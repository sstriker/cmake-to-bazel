// bb-worker config: pulls actions from the scheduler, materializes
// the input root onto the shared build volume, calls bb-runner-bare
// over a UNIX socket to execute, packages outputs back to CAS.
//
// Platform properties MUST match what the orchestrator's
// defaultPlatform / --platform flag declares. Mismatched properties
// mean the scheduler never assigns work to this worker.

{
  global: {
    diagnosticsHttpServer: {
      listenAddress: ':9981',
      enablePrometheus: true,
    },
  },
  blobstore: {
    contentAddressableStorage: {
      grpc: { address: 'bb-storage:8980' },
    },
    actionCache: {
      grpc: { address: 'bb-storage:8980' },
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
      // Worker pool name (humans see this in bb-browser); not used
      // for routing in this single-pool setup.
      workerId: { pool: 'cmake-to-bazel' },
      sizeClass: 0,
      defaultExecutionTimeout: '1800s',
      maximumExecutionTimeout: '3600s',
    }],
  }],
}
