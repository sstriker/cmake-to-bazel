# Buildbarn validation deployment

A single-node Buildbarn instance for validating the orchestrator's M5
REAPI cache substrate against real Buildbarn code (vs the in-process
fake the unit tests use).

## Scope

- `bb-storage` only — CAS + ActionCache. Sufficient to validate M5's
  cache-share claim end to end.
- No execution service. M3b's `--execute` path needs `bb-scheduler` +
  `bb-runner-bare`; queued as a separate deployment.
- No authentication. Localhost-only port mapping.
- File-backed blobstore at 1 GiB (CAS) + 64 MiB (AC). Tear down with
  `docker compose down -v` to wipe.

## Bring up

```sh
docker compose -f deploy/buildbarn/docker-compose.yml up -d
# wait for healthcheck
until curl -fsS http://127.0.0.1:9980/-/healthy >/dev/null; do sleep 1; done
```

## Validate

```sh
make e2e-buildbarn
```

This runs the orchestrator twice against the fdsdk-subset fixture,
both times pointed at `grpc://127.0.0.1:8980`:

1. Cold pass populates Buildbarn's CAS+AC.
2. Second pass with a clean tmpdir hits AC for every element and
   produces byte-identical outputs.

This is the same architectural claim the in-process-fake
`TestRun_CacheShare_TwoOrchestratorsViaSharedAC` test makes, now
proven against real Buildbarn.

## Tear down

```sh
docker compose -f deploy/buildbarn/docker-compose.yml down -v
```

`-v` removes the named volume so a re-run starts cold. Omit `-v` to
inspect a populated CAS across runs (e.g. via `bb-browser`, not
deployed here).

## Schema drift

The bb-storage image version in `docker-compose.yml` is pinned. When
bumping it, reconcile `config/storage.jsonnet` against:

- [`blobstore.proto`](https://github.com/buildbarn/bb-storage/blob/master/pkg/proto/configuration/blobstore/blobstore.proto)
- [`global.proto`](https://github.com/buildbarn/bb-storage/blob/master/pkg/proto/configuration/global/global.proto)

Test schema changes by running `make e2e-buildbarn` against the new
image before merging.
