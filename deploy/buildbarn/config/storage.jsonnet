// bb-storage configuration for the orchestrator's M5 cache-substrate
// validation. Single-node, file-backed blobstore, no auth.
//
// This config is deliberately minimal — production deployments use
// jsonnet imports to share patterns across the bb-* services and add
// authentication, sharding, and replication. We don't need any of
// that to validate the REAPI protocol round trip.
//
// Schema drift: this config is shaped against the bb-storage image
// version pinned in docker-compose.yml. Bumping the image requires
// reconciling against
//   https://github.com/buildbarn/bb-storage/blob/master/pkg/proto/configuration/blobstore/blobstore.proto
// for the blobstore section and
//   https://github.com/buildbarn/bb-storage/blob/master/pkg/proto/configuration/global/global.proto
// for `global`.

{
  global: {
    diagnosticsHttpServer: {
      listenAddress: ':9980',
      enablePrometheus: true,
      enablePprof: true,
    },
  },
  blobstore: {
    contentAddressableStorage: {
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
    actionCache: {
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
      },
    },
  },
  grpcServers: [{
    listenAddresses: [':8980'],
    authenticationPolicy: { allow: {} },
  }],
  maximumMessageSizeBytes: 16 * 1024 * 1024,
}
