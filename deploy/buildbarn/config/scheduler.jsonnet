// bb-scheduler config: queues Actions submitted via the Execution
// service and dispatches them to bb-worker instances pulling from
// the scheduler.
//
// The simplest viable config: in-memory ActionRouter (no platform-
// queue partitioning), single worker pool, no auth.

{
  global: {
    diagnosticsHttpServer: {
      listenAddress: ':9982',
      enablePrometheus: true,
    },
  },
  contentAddressableStorage: {
    grpc: { address: 'bb-storage:8980' },
  },
  actionCache: {
    grpc: { address: 'bb-storage:8980' },
  },
  // Client-facing gRPC (Execution + Capabilities). The orchestrator
  // submits Action digests here.
  clientGrpcServers: [{
    listenAddresses: [':8983'],
    authenticationPolicy: { allow: {} },
  }],
  // Worker-facing gRPC. bb-worker connects here to pull queued
  // actions and report results.
  workerGrpcServers: [{
    listenAddresses: [':8984'],
    authenticationPolicy: { allow: {} },
  }],
  // Single in-memory work queue, pretty operator name shown in
  // bb-browser. Production deployments shard by platform / size class.
  inMemoryBuildQueue: {
    executionUpdateInterval: '1s',
    operationParkingDuration: '60s',
    platformQueueWithNoWorkersTimeout: '15m',
    busyWorkerSynchronizationInterval: '10s',
    workerTaskRetryCount: 9,
    workerWithNoSynchronizationsTimeout: '60s',
  },
  maximumMessageSizeBytes: 16 * 1024 * 1024,
}
