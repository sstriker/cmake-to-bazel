// bb-runner-bare config: actually exec's commands inside the build
// directory bb-worker hands it. Bare (no chroot, no jail) — this is
// the simplest deployment that works without privileged containers.
//
// In production you'd use bb-runner-installer + bb-noop-worker for
// a properly sandboxed execution; bare is fine for our localhost
// validation since the input roots are content-addressed and the
// worker container itself is the isolation boundary.

{
  global: {
    diagnosticsHttpServer: {
      listenAddress: ':9985',
      enablePrometheus: true,
    },
  },
  buildDirectoryPath: '/worker/build',
  grpcServers: [{
    listenPaths: ['/worker/build/runner.sock'],
    authenticationPolicy: { allow: {} },
  }],
  maximumMessageSizeBytes: 16 * 1024 * 1024,
}
