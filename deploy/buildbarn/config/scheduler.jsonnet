// bb-scheduler config: queues Actions submitted via the Execution
// service and dispatches them to bb-worker instances pulling from
// the scheduler.
//
// Schema reference (master, matches the 4415657 image tag):
//   https://github.com/buildbarn/bb-remote-execution/blob/master/pkg/proto/configuration/bb_scheduler/bb_scheduler.proto
//
// Top-level fields the proto recognises (subset we use):
//   - client_grpc_servers   (Execute / Capabilities served to bazel/orchestrator)
//   - worker_grpc_servers   (synchronize / pull served to bb-worker)
//   - content_addressable_storage  (BlobAccessConfiguration — for Action lookup)
//   - action_router         (queue-routing + size-class policy)
//   - execute_authorizer / modify_drains_authorizer / kill_operations_authorizer
//     / synchronize_authorizer  (all required, even if {allow:{}})
//   - platform_queue_with_no_workers_timeout
//   - global / maximum_message_size_bytes
//
// Notably NOT here:
//   - actionCache: the scheduler doesn't directly read AC; the worker
//     populates it after execution. Earlier schemas had this field;
//     it's gone.
//   - inMemoryBuildQueue: replaced by action_router (its size-class
//     analyzer carries the timeout knobs).
//   - default_execution_timeout / maximum_execution_timeout on the
//     worker: they're scheduler-side now, inside action_router.

{
  global: {
    diagnosticsHttpServer: {
      httpServers: [{
        listenAddresses: [':9982'],
        authenticationPolicy: { allow: {} },
      }],
      enablePrometheus: true,
    },
  },
  // CAS lookup for Action protos. BlobAccessConfiguration's grpc
  // backend wants a nested `client` (ClientConfiguration), not a
  // bare `address` field.
  contentAddressableStorage: {
    grpc: { client: { address: 'bb-storage:8980' } },
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
  executeAuthorizer: { allow: {} },
  modifyDrainsAuthorizer: { allow: {} },
  killOperationsAuthorizer: { allow: {} },
  synchronizeAuthorizer: { allow: {} },
  // Single-tier router: extract platform keys directly from the Action
  // and use the bazel correlated_invocations_id + tool_invocation_id
  // for grouping. The size-class analyzer carries the execution
  // timeout knobs that used to live on the worker.
  actionRouter: {
    simple: {
      platformKeyExtractor: { action: {} },
      invocationKeyExtractors: [
        { correlatedInvocationsId: {} },
        { toolInvocationId: {} },
      ],
      initialSizeClassAnalyzer: {
        defaultExecutionTimeout: '1800s',
        maximumExecutionTimeout: '7200s',
      },
    },
  },
  platformQueueWithNoWorkersTimeout: '900s',
  maximumMessageSizeBytes: 16 * 1024 * 1024,
}
