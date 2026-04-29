// bb-runner-bare config: actually exec's commands inside the build
// directory bb-worker hands it. Bare (no chroot, no jail) — this is
// the simplest deployment that works without privileged containers.
//
// In production you'd use bb-runner-installer + bb-noop-worker for
// a properly sandboxed execution; bare is fine for our localhost
// validation since the input roots are content-addressed and the
// worker container itself is the isolation boundary.
//
// global.diagnosticsHttpServer wants a list of httpServers (each
// with its own listenAddresses + authenticationPolicy); the older
// single listen_address field is reserved in the proto.
//
// bb_runner's ApplicationConfiguration has no top-level
// `maximum_message_size_bytes` field — proto unmarshal fails with
// "unknown field 'maximumMessageSizeBytes'" if set. The relevant
// limit lives per-server as
// grpcServers[].maximumReceivedMessageSizeBytes (on the
// grpc.ServerConfiguration message). bb-runner only exec's bb-worker's
// commands and doesn't carry large payloads itself, so we use the
// server default (4 MiB suffices for command/argv/env/output paths)
// and don't override.
//
// listenPaths MUST be outside buildDirectoryPath. bb_runner clears
// stale entries from its build directory at startup (and during
// build action lifecycle); placing the socket inside that tree
// causes bb_runner to unlink its own listening socket immediately
// after binding it. bb-worker's dial then fails with
// "dial unix .../runner.sock: connect: no such file or directory".
// Upstream bb-deployments keeps the socket in /worker/runner
// (sibling of /worker/build); we follow the same convention via a
// dedicated /sock volume shared between bb-worker and bb-runner-bare.

{
  global: {
    diagnosticsHttpServer: {
      httpServers: [{
        listenAddresses: [':9985'],
        authenticationPolicy: { allow: {} },
      }],
      enablePrometheus: true,
    },
  },
  buildDirectoryPath: '/worker/build',
  grpcServers: [{
    listenPaths: ['/sock/runner.sock'],
    authenticationPolicy: { allow: {} },
  }],
}
