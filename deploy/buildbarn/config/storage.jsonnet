// bb-storage configuration for the orchestrator's M5 cache-substrate
// validation. Single-node, file-backed blobstore, no auth.
//
// Top-level shape matches bb-storage's ApplicationConfiguration proto
// (pkg/proto/configuration/bb_storage/bb_storage.proto):
//
//   - `content_addressable_storage` (field 17) and `action_cache`
//     (field 18) are direct top-level fields, NOT nested under a
//     `blobstore` field. Field 1 (the old `blobstore` wrapper) is
//     reserved in the proto; bb-storage rejects configs that use it
//     with "unknown field 'blobstore'".
//   - Each carries a `backend` BlobAccessConfiguration plus required
//     authorizer fields (`get_authorizer`, `put_authorizer`, and for
//     CAS, `find_missing_authorizer`).
//
// This shape mirrors buildbarn/bb-deployments@main:
//   docker-compose/config/storage.jsonnet
//
// Schema reference for upgrades:
//   https://github.com/buildbarn/bb-storage/blob/master/pkg/proto/configuration/bb_storage/bb_storage.proto
//   https://github.com/buildbarn/bb-storage/blob/master/pkg/proto/configuration/blobstore/blobstore.proto

{
  global: {
    diagnosticsHttpServer: {
      // Newer schema: diagnosticsHttpServer takes a list of HTTP servers,
      // not a single listenAddress string. Each server has its own
      // listenAddresses + authenticationPolicy. Mirrors the gRPC
      // server shape.
      httpServers: [{
        listenAddresses: [':9980'],
        authenticationPolicy: { allow: {} },
      }],
      enablePrometheus: true,
      enablePprof: true,
    },
  },
  contentAddressableStorage: {
    backend: {
      'local': {
        keyLocationMapInMemory: { entries: 100000 },
        keyLocationMapMaximumGetAttempts: 16,
        keyLocationMapMaximumPutAttempts: 64,
        oldBlocks: 8,
        currentBlocks: 24,
        newBlocks: 3,
        blocksOnBlockDevice: {
          source: {
            file: {
              path: '/storage/cas',
              sizeBytes: 1073741824,  // 1 GiB
            },
          },
          spareBlocks: 3,
        },
        persistent: {
          stateDirectoryPath: '/storage/cas-state',
          minimumEpochInterval: '300s',
        },
      },
    },
    getAuthorizer: { allow: {} },
    putAuthorizer: { allow: {} },
    findMissingAuthorizer: { allow: {} },
  },
  actionCache: {
    // Bazel clients require completenessChecking on AC reads — they
    // depend on every output blob a cached ActionResult references
    // being still present in CAS. Wrapping the local backend with the
    // completenessChecking decorator guarantees this.
    backend: {
      completenessChecking: {
        backend: {
          'local': {
            keyLocationMapInMemory: { entries: 10000 },
            keyLocationMapMaximumGetAttempts: 16,
            keyLocationMapMaximumPutAttempts: 64,
            oldBlocks: 4,
            currentBlocks: 4,
            newBlocks: 1,
            blocksOnBlockDevice: {
              source: {
                file: {
                  path: '/storage/ac',
                  sizeBytes: 67108864,  // 64 MiB
                },
              },
              spareBlocks: 1,
            },
            persistent: {
              stateDirectoryPath: '/storage/ac-state',
              minimumEpochInterval: '300s',
            },
          },
        },
        maximumTotalTreeSizeBytes: 64 * 1024 * 1024,
      },
    },
    getAuthorizer: { allow: {} },
    putAuthorizer: { allow: {} },
  },
  grpcServers: [{
    listenAddresses: [':8980'],
    authenticationPolicy: { allow: {} },
  }],
  maximumMessageSizeBytes: 16 * 1024 * 1024,
}
