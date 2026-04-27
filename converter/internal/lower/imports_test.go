package lower_test

import (
	"errors"
	"testing"

	"github.com/sstriker/cmake-to-bazel/converter/internal/failure"
	"github.com/sstriker/cmake-to-bazel/converter/internal/fileapi"
	"github.com/sstriker/cmake-to-bazel/converter/internal/lower"
	"github.com/sstriker/cmake-to-bazel/internal/manifest"
)

// TestToIR_CrossElementDep_ResolvedViaManifest exercises the manifest
// fallback in the dep-resolution chain: a synthetic Reply where target T
// depends on a target id that isn't in r.Targets but resolves to a
// CMakeTarget in the manifest. The dep is rewritten to the bazel_label.
func TestToIR_CrossElementDep_ResolvedViaManifest(t *testing.T) {
	r := &fileapi.Reply{
		Codemodel: fileapi.Codemodel{
			Configurations: []fileapi.Configuration{{
				Name:    "Release",
				Targets: []fileapi.ConfigTargetRef{{Name: "client", Id: "client::@1"}},
			}},
		},
		Targets: map[string]fileapi.Target{
			"client::@1": {
				Name:    "client",
				Type:    "STATIC_LIBRARY",
				Sources: []fileapi.TargetSource{{Path: "client.c", CompileGroupIndex: 0}},
				CompileGroups: []fileapi.CompileGroup{{
					Language:      "C",
					SourceIndexes: []int{0},
				}},
				Dependencies: []fileapi.TargetDependency{
					// In-element ids hash on `::@`. Cross-element ones use
					// the same shape — only the namespace prefix differs.
					{Id: "Glibc::c::@somehash"},
				},
			},
		},
	}
	rsv, err := manifest.Index(&manifest.Imports{
		Version: 1,
		Elements: []*manifest.Element{{
			Name: "elem_glibc",
			Exports: []*manifest.Export{{
				CMakeTarget: "Glibc::c",
				BazelLabel:  "@elem_glibc//:c",
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	pkg, err := lower.ToIR(r, nil, lower.Options{
		HostSourceRoot: "/nonexistent",
		Imports:        rsv,
	})
	if err != nil {
		t.Fatalf("ToIR: %v", err)
	}
	if len(pkg.Targets) != 1 {
		t.Fatalf("Targets = %d, want 1", len(pkg.Targets))
	}
	deps := pkg.Targets[0].Deps
	if len(deps) != 1 || deps[0] != "@elem_glibc//:c" {
		t.Errorf("Deps = %v, want [@elem_glibc//:c]", deps)
	}
}

// TestToIR_CrossElementDep_UnresolvedFails verifies that a dep id absent from
// both the in-element table and the imports manifest produces a typed
// unresolved-link-dep error rather than silently dropping.
func TestToIR_CrossElementDep_UnresolvedFails(t *testing.T) {
	r := &fileapi.Reply{
		Codemodel: fileapi.Codemodel{
			Configurations: []fileapi.Configuration{{
				Name:    "Release",
				Targets: []fileapi.ConfigTargetRef{{Name: "client", Id: "client::@1"}},
			}},
		},
		Targets: map[string]fileapi.Target{
			"client::@1": {
				Name:    "client",
				Type:    "STATIC_LIBRARY",
				Sources: []fileapi.TargetSource{{Path: "client.c", CompileGroupIndex: 0}},
				CompileGroups: []fileapi.CompileGroup{{
					Language: "C", SourceIndexes: []int{0},
				}},
				Dependencies: []fileapi.TargetDependency{
					{Id: "Mystery::lib::@h"},
				},
			},
		},
	}
	_, err := lower.ToIR(r, nil, lower.Options{HostSourceRoot: "/nonexistent"})
	if err == nil {
		t.Fatal("expected unresolved-link-dep")
	}
	var fe *failure.Error
	if !errors.As(err, &fe) || fe.Code != failure.UnresolvedLinkDep {
		t.Errorf("err = %v, want unresolved-link-dep", err)
	}
}
