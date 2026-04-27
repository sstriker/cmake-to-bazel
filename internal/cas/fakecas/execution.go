package fakecas

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"

	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	longrunning "google.golang.org/genproto/googleapis/longrunning"
	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

func digestOf(b []byte) *repb.Digest {
	sum := sha256.Sum256(b)
	return &repb.Digest{
		Hash:      hex.EncodeToString(sum[:]),
		SizeBytes: int64(len(b)),
	}
}

func sortDirectory(d *repb.Directory) {
	sort.Slice(d.Files, func(i, j int) bool { return d.Files[i].Name < d.Files[j].Name })
	sort.Slice(d.Directories, func(i, j int) bool { return d.Directories[i].Name < d.Directories[j].Name })
	sort.Slice(d.Symlinks, func(i, j int) bool { return d.Symlinks[i].Name < d.Symlinks[j].Name })
}

// ExecutionServer is a worker stand-in: it pulls Action+Command from
// the Server's CAS, materializes the input root, forks the Command's
// argv locally, packages outputs back into CAS, and returns an
// ActionResult.
//
// It uses the SAME Server for state, so the in-memory CAS is shared
// between the Execution service and the CAS/AC services — workers
// need to read Action/Command/inputs from the same store the client
// uploaded them to. Production REAPI deployments share a CAS endpoint
// for exactly this reason.
type ExecutionServer struct {
	repb.UnimplementedExecutionServer
	server *Server
	count  atomic.Int64

	// SkipCacheLookup forces every Execute call to actually run the
	// command, even when the AC has an entry for action_digest. Useful
	// for tests that want to assert the worker ran.
	SkipCacheLookup bool
}

// NewExecutionServer wires a fake worker on top of an existing
// CAS/AC Server. The same Server's blobs map is the source of truth
// for inputs and the destination for outputs.
func NewExecutionServer(server *Server) *ExecutionServer {
	return &ExecutionServer{server: server}
}

// ExecuteCount reports how many Execute calls have actually forked the
// command (cache hits don't count). Useful for tests asserting the
// worker DID run, or DID NOT.
func (e *ExecutionServer) ExecuteCount() int64 {
	return e.count.Load()
}

// Execute implements the REAPI service. Streams Operations until done.
// The "Done" message carries an ExecuteResponse Any-wrapping the
// ActionResult.
func (e *ExecutionServer) Execute(req *repb.ExecuteRequest, stream repb.Execution_ExecuteServer) error {
	ctx := stream.Context()

	// Honor cached results unless the test forces a cache miss.
	if !req.SkipCacheLookup && !e.SkipCacheLookup {
		if ar, ok := e.cachedResult(req.ActionDigest); ok {
			return e.sendDone(stream, ar, true)
		}
	}

	ar, err := e.runAction(ctx, req.ActionDigest)
	if err != nil {
		return e.sendDoneError(stream, err)
	}

	// Workers publish their result to the AC; the M5 plan's Tier-3
	// resilience case relies on this being the worker's job, not the
	// client's.
	e.server.mu.Lock()
	e.server.action[req.ActionDigest.Hash] = proto.Clone(ar).(*repb.ActionResult)
	e.server.mu.Unlock()

	return e.sendDone(stream, ar, false)
}

func (e *ExecutionServer) cachedResult(d *repb.Digest) (*repb.ActionResult, bool) {
	e.server.mu.Lock()
	defer e.server.mu.Unlock()
	ar, ok := e.server.action[d.Hash]
	if !ok {
		return nil, false
	}
	return proto.Clone(ar).(*repb.ActionResult), true
}

// runAction is the actual worker. Reads Action+Command, materializes
// the input root, forks, captures outputs.
func (e *ExecutionServer) runAction(ctx context.Context, actionDigest *repb.Digest) (*repb.ActionResult, error) {
	e.count.Add(1)

	action := &repb.Action{}
	if err := e.unmarshalBlob(actionDigest, action); err != nil {
		return nil, fmt.Errorf("read action: %w", err)
	}
	cmd := &repb.Command{}
	if err := e.unmarshalBlob(action.CommandDigest, cmd); err != nil {
		return nil, fmt.Errorf("read command: %w", err)
	}

	tmp, err := os.MkdirTemp("", "fakeworker-*")
	if err != nil {
		return nil, fmt.Errorf("mkdir worker root: %w", err)
	}
	defer os.RemoveAll(tmp)

	if err := e.materializeDir(action.InputRootDigest, tmp); err != nil {
		return nil, fmt.Errorf("materialize input root: %w", err)
	}

	if len(cmd.Arguments) == 0 {
		return nil, errors.New("Command has no arguments")
	}
	argv0 := cmd.Arguments[0]
	if !filepath.IsAbs(argv0) {
		argv0 = filepath.Join(tmp, cmd.WorkingDirectory, argv0)
	}
	wd := filepath.Join(tmp, cmd.WorkingDirectory)

	stdoutBuf, stderrBuf := &bytes.Buffer{}, &bytes.Buffer{}
	c := exec.CommandContext(ctx, argv0, cmd.Arguments[1:]...)
	c.Dir = wd
	c.Stdout = stdoutBuf
	c.Stderr = stderrBuf
	c.Env = envFromCommand(cmd)

	exitCode := int32(0)
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = int32(ee.ExitCode())
		} else {
			return nil, fmt.Errorf("worker exec: %w", err)
		}
	}

	ar := &repb.ActionResult{ExitCode: exitCode}
	if stdoutBuf.Len() > 0 {
		body := stdoutBuf.Bytes()
		d := digestOf(body)
		e.putBlob(d, body)
		ar.StdoutDigest = d
	}
	if stderrBuf.Len() > 0 {
		body := stderrBuf.Bytes()
		d := digestOf(body)
		e.putBlob(d, body)
		ar.StderrDigest = d
	}

	for _, rel := range cmd.OutputPaths {
		host := filepath.Join(wd, rel)
		info, err := os.Stat(host)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("stat output %s: %w", host, err)
		}
		switch {
		case info.Mode().IsRegular():
			body, err := os.ReadFile(host)
			if err != nil {
				return nil, err
			}
			d := digestOf(body)
			e.putBlob(d, body)
			ar.OutputFiles = append(ar.OutputFiles, &repb.OutputFile{
				Path:         filepath.ToSlash(rel),
				Digest:       d,
				IsExecutable: info.Mode()&0o111 != 0,
			})
		case info.IsDir():
			treeDigest, err := e.packAndUploadTree(host)
			if err != nil {
				return nil, fmt.Errorf("pack output %s: %w", rel, err)
			}
			ar.OutputDirectories = append(ar.OutputDirectories, &repb.OutputDirectory{
				Path:       filepath.ToSlash(rel),
				TreeDigest: treeDigest,
			})
		}
	}
	return ar, nil
}

// materializeDir walks a Directory referenced by digest and writes
// every file/symlink/subdir into dst.
func (e *ExecutionServer) materializeDir(d *repb.Digest, dst string) error {
	body, ok := e.getBlob(d)
	if !ok {
		return fmt.Errorf("materialize: directory %s missing in CAS", d.Hash)
	}
	dir := &repb.Directory{}
	if err := proto.Unmarshal(body, dir); err != nil {
		return fmt.Errorf("materialize: unmarshal directory: %w", err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, f := range dir.Files {
		fb, ok := e.getBlob(f.Digest)
		if !ok {
			return fmt.Errorf("materialize: file %s missing in CAS", f.Digest.Hash)
		}
		mode := os.FileMode(0o644)
		if f.IsExecutable {
			mode = 0o755
		}
		if err := os.WriteFile(filepath.Join(dst, f.Name), fb, mode); err != nil {
			return err
		}
	}
	for _, sl := range dir.Symlinks {
		if err := os.Symlink(sl.Target, filepath.Join(dst, sl.Name)); err != nil {
			return err
		}
	}
	for _, sub := range dir.Directories {
		if err := e.materializeDir(sub.Digest, filepath.Join(dst, sub.Name)); err != nil {
			return err
		}
	}
	return nil
}

// packAndUploadTree walks a local directory, builds a Tree proto with
// every Directory + file blob inline, uploads them all, and returns
// the Tree digest for the OutputDirectory entry.
func (e *ExecutionServer) packAndUploadTree(root string) (*repb.Digest, error) {
	rootDir, _, err := e.packSubtree(root, true)
	if err != nil {
		return nil, err
	}
	tree := &repb.Tree{Root: rootDir}
	// Collect children: every Directory we walked except the root.
	if err := e.collectChildren(root, rootDir, tree); err != nil {
		return nil, err
	}
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(tree)
	if err != nil {
		return nil, err
	}
	d := digestOf(body)
	e.putBlob(d, body)
	return d, nil
}

func (e *ExecutionServer) packSubtree(absPath string, upload bool) (*repb.Directory, *repb.Digest, error) {
	entries, err := os.ReadDir(absPath)
	if err != nil {
		return nil, nil, err
	}
	dir := &repb.Directory{}
	for _, entry := range entries {
		name := entry.Name()
		child := filepath.Join(absPath, name)
		info, err := entry.Info()
		if err != nil {
			return nil, nil, err
		}
		switch {
		case info.Mode().IsRegular():
			body, err := os.ReadFile(child)
			if err != nil {
				return nil, nil, err
			}
			d := digestOf(body)
			if upload {
				e.putBlob(d, body)
			}
			dir.Files = append(dir.Files, &repb.FileNode{
				Name:         name,
				Digest:       d,
				IsExecutable: info.Mode()&0o111 != 0,
			})
		case info.IsDir():
			subDir, subDigest, err := e.packSubtree(child, upload)
			if err != nil {
				return nil, nil, err
			}
			_ = subDir
			dir.Directories = append(dir.Directories, &repb.DirectoryNode{
				Name:   name,
				Digest: subDigest,
			})
		}
	}
	sortDirectory(dir)
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(dir)
	if err != nil {
		return nil, nil, err
	}
	if upload {
		e.putBlob(digestOf(body), body)
	}
	return dir, digestOf(body), nil
}

func (e *ExecutionServer) collectChildren(root string, rootDir *repb.Directory, tree *repb.Tree) error {
	// Walk every Directory under root and append to tree.Children
	// (excluding the root itself). The simplest approach: re-walk the
	// filesystem and pack each subdir.
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() || p == root {
			return nil
		}
		sub, _, err := e.packSubtree(p, false)
		if err != nil {
			return err
		}
		tree.Children = append(tree.Children, sub)
		return nil
	})
}

func (e *ExecutionServer) getBlob(d *repb.Digest) ([]byte, bool) {
	e.server.mu.Lock()
	defer e.server.mu.Unlock()
	body, ok := e.server.blobs[d.Hash]
	return body, ok
}

func (e *ExecutionServer) putBlob(d *repb.Digest, body []byte) {
	e.server.mu.Lock()
	e.server.blobs[d.Hash] = append([]byte(nil), body...)
	e.server.mu.Unlock()
}

func (e *ExecutionServer) unmarshalBlob(d *repb.Digest, m proto.Message) error {
	body, ok := e.getBlob(d)
	if !ok {
		return fmt.Errorf("blob %s missing", d.Hash)
	}
	return proto.Unmarshal(body, m)
}

// sendDone wraps an ActionResult in an ExecuteResponse-bearing
// longrunning.Operation with done=true.
func (e *ExecutionServer) sendDone(stream repb.Execution_ExecuteServer, ar *repb.ActionResult, cached bool) error {
	resp := &repb.ExecuteResponse{
		Result:       ar,
		CachedResult: cached,
	}
	any, err := anypb.New(resp)
	if err != nil {
		return err
	}
	op := &longrunning.Operation{
		Name: "fakeworker/" + strings.TrimSpace(ar.GetExecutionMetadata().GetWorker()),
		Done: true,
		Result: &longrunning.Operation_Response{
			Response: any,
		},
	}
	return stream.Send(op)
}

func (e *ExecutionServer) sendDoneError(stream repb.Execution_ExecuteServer, runErr error) error {
	op := &longrunning.Operation{
		Name: "fakeworker/error",
		Done: true,
		Result: &longrunning.Operation_Error{
			Error: &rpcstatus.Status{
				Code:    int32(codes.Internal),
				Message: runErr.Error(),
			},
		},
	}
	if err := stream.Send(op); err != nil {
		return err
	}
	return status.Errorf(codes.Internal, "%v", runErr)
}

// envFromCommand projects Command.environment_variables to an os/exec
// Env slice ("KEY=VAL"). Inheriting the host PATH if not declared by
// the action is convenient for tests; production workers wouldn't.
func envFromCommand(cmd *repb.Command) []string {
	have := make(map[string]bool)
	out := make([]string, 0, len(cmd.EnvironmentVariables)+1)
	for _, ev := range cmd.EnvironmentVariables {
		out = append(out, ev.Name+"="+ev.Value)
		have[ev.Name] = true
	}
	if !have["PATH"] {
		if hostPath := os.Getenv("PATH"); hostPath != "" {
			out = append(out, "PATH="+hostPath)
		}
	}
	return out
}
