// bb-worker config: pulls actions from the scheduler, materializes
// the input root onto the shared build volume, calls bb-runner-bare
// over a UNIX socket to execute, packages outputs back to CAS.
//
// Platform properties MUST match what the orchestrator's
// defaultPlatform / --platform flag declares. Mismatched properties
// mean the scheduler never assigns work to this worker.
//
// Schema reference:
//   https://github.com/buildbarn/bb-remote-execution/blob/master/pkg/proto/configuration/bb_worker/bb_worker.proto
//
// Notable schema notes:
//   - blobstore.{contentAddressableStorage,actionCache} are
//     BlobAccessConfigurations; the grpc backend takes
//     `client: { address: ... }`, not bare `address`.
//   - default_execution_timeout / maximum_execution_timeout used to
//     live on each runner; they were moved to the scheduler's
//     action_router.initial_size_class_analyzer. Setting them here
//     fails proto unmarshal with "unknown field".
//   - global.diagnosticsHttpServer takes a list of httpServers, not
//     a single listen_address.

{
  global: {
    diagnosticsHttpServer: {
      httpServers: [{
        listenAddresses: [':9981'],
        authenticationPolicy: { allow: {} },
      }],
      enablePrometheus: true,
    },
  },
  blobstore: {
    contentAddressableStorage: {
      grpc: { client: { address: 'bb-storage:8980' } },
    },
    actionCache: {
      grpc: { client: { address: 'bb-storage:8980' } },
    },
  },
  scheduler: { address: 'bb-scheduler:8984' },
  browserUrl: 'http://localhost:7984/',
  maximumMessageSizeBytes: 16 * 1024 * 1024,
  // The build root the runner shares — bb-worker writes input roots
  // here and bb-runner-bare exec's commands inside subdirectories.
  buildDirectories: [{
    native: {
      buildDirectoryPath: '/worker/build',
      cacheDirectoryPath: '/worker/cache',
      maximumCacheFileCount: 10000,
      maximumCacheSizeBytes: 1024 * 1024 * 1024,
      cacheReplacementPolicy: 'LEAST_RECENTLY_USED',
    },
    runners: [{
      endpoint: { address: 'unix:///worker/build/runner.sock' },
      concurrency: 1,
      // Platform properties mirror orchestrator's defaultPlatform.
      // Operators bumping any pin must update both sides.
      platform: {
        properties: [
          { name: 'Arch', value: 'x86_64' },
          { name: 'OSFamily', value: 'linux' },
          { name: 'bwrap-version', value: '0.8.0' },
          { name: 'cmake-version', value: '3.28.3' },
          { name: 'ninja-version', value: '1.11.1' },
        ],
      },
      // workerId is a map<string,string> in the proto; values surface
      // in bb-browser for operators distinguishing replicas.
      workerId: {
        pool: 'cmake-to-bazel',
      },
      sizeClass: 0,
    }],
  }],
}
