.PHONY: all converter orchestrator diff history bst-translate derive-toolchain test test-e2e e2e-hello-world e2e-fmt \
        e2e-orchestrate e2e-orchestrate-scale e2e-bazel-build e2e-cmake-consumer e2e-toolchain-skip e2e-fidelity e2e-fidelity-fmt e2e-buildbarn e2e-buildbarn-execute \
        buildbarn-up buildbarn-down \
        fetch-fmt update-golden record-fixtures lint vet fmt check-tools clean

# Pinned external tool versions. Hard-failed at runtime by the converter,
# enforced softly here for dev-loop visibility.
CMAKE_VERSION  ?= 3.28.3
NINJA_VERSION  ?= 1.11.1
BWRAP_VERSION  ?= 0.8.0

# M2 acceptance-package version. Bumping requires a re-run of
# TestE2E_Fmt_Converts since the *_test count assertion has a floor.
FMT_VERSION    ?= 11.0.2
FMT_DIR        ?= /tmp/fmt

GO        ?= go
GOFLAGS   ?=
BUILD_DIR ?= build
BIN_DIR   := $(BUILD_DIR)/bin

CONVERTER    := $(BIN_DIR)/convert-element
ORCHESTRATOR := $(BIN_DIR)/orchestrate
DIFF         := $(BIN_DIR)/orchestrate-diff
HISTORY      := $(BIN_DIR)/orchestrate-history
BST_TRANSLATE := $(BIN_DIR)/orchestrate-bst-translate
DERIVE_TOOLCHAIN := $(BIN_DIR)/derive-toolchain

all: converter orchestrator diff history bst-translate derive-toolchain

converter: $(CONVERTER)

orchestrator: $(ORCHESTRATOR)

diff: $(DIFF)

history: $(HISTORY)

bst-translate: $(BST_TRANSLATE)

derive-toolchain: $(DERIVE_TOOLCHAIN)

$(CONVERTER):
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -o $(CONVERTER) ./converter/cmd/convert-element

$(ORCHESTRATOR):
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -o $(ORCHESTRATOR) ./orchestrator/cmd/orchestrate

$(DIFF):
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -o $(DIFF) ./orchestrator/cmd/orchestrate-diff

$(HISTORY):
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -o $(HISTORY) ./orchestrator/cmd/orchestrate-history

$(BST_TRANSLATE):
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -o $(BST_TRANSLATE) ./orchestrator/cmd/orchestrate-bst-translate

$(DERIVE_TOOLCHAIN):
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -o $(DERIVE_TOOLCHAIN) ./converter/cmd/derive-toolchain

# Unit tests: pre-recorded File API fixtures, no cmake required.
test:
	$(GO) test ./...

# End-to-end tests: real cmake + bwrap. Gated behind build tag.
test-e2e: check-tools converter
	$(GO) test -tags=e2e ./converter/...

e2e-hello-world: check-tools converter
	$(GO) test -tags=e2e -run TestE2E_HelloWorld ./converter/...

e2e-fmt: check-tools converter fetch-fmt
	$(GO) test -tags=e2e -run TestE2E_Fmt ./converter/...

e2e-orchestrate: check-tools converter orchestrator
	$(GO) test -tags=e2e -run TestE2E_Orchestrate ./orchestrator/...

# Scale-fixture concurrency gate. Drives the orchestrator over a 50-element
# synthetic graph (orchestrator/testdata/fdsdk-scale/) at concurrency=1/8/32
# and asserts byte-identical per-element outputs across levels. Surfaces AC
# eviction races, queue-depth imbalances, and goroutine-pool bugs that the
# 3-element fdsdk-subset can't exercise. Uses the test binary's stub
# converter — no cmake/bwrap/ninja needed.
e2e-orchestrate-scale: orchestrator
	$(GO) test -run TestRun_Scale_DeterministicAcrossLevels -timeout 300s ./orchestrator/internal/orchestrator/...

# M5 downstream-build acceptance gate. Requires bazel/bazelisk on PATH
# in addition to the standard cmake/ninja/bwrap; if absent the test
# self-skips (runtime LookPath check). Spins the orchestrator against
# the FDSDK subset, then runs `bazel build //:smoke` against a
# downstream consumer that depends on a converted element.
e2e-bazel-build: check-tools converter orchestrator
	$(GO) test -tags=e2e -run TestE2E_BazelBuild ./orchestrator/...

# M5 CMake-side acceptance gate. Configures a downstream find_package
# consumer against the orchestrator's synth-prefix tree. No bazel
# required; just real cmake + bwrap (already covered by check-tools).
e2e-cmake-consumer: check-tools converter orchestrator
	$(GO) test -tags=e2e -run TestE2E_CMakeConsumer ./orchestrator/...

# Toolchain configure-skip e2e: runs the orchestrator twice against the
# fdsdk-subset (without and with --toolchain-cmake-file) and asserts
# the second pass's cumulative cmake-configure wall-clock is shorter.
# Validates the derive-toolchain -> toolchain.cmake -> cmakerun
# integration end-to-end.
e2e-toolchain-skip: check-tools converter orchestrator derive-toolchain
	$(GO) test -tags=e2e -run TestE2E_Toolchain_SkipReducesConfigureTime ./orchestrator/...

# Fidelity gate. Parameterized harness: hello-world is the smoke
# fixture; fmt (when fetched via `fetch-fmt`) is the real-world
# fixture. Each fixture builds the project two ways (cmake reference
# vs convert-element + bazel) and asserts symbol equivalence on the
# resulting library. Each new delta is recorded in
# docs/fidelity-known-deltas.md.
e2e-fidelity: check-tools converter
	$(GO) test -tags=e2e -run TestE2E_Fidelity ./orchestrator/...

# Same as e2e-fidelity but ensures the fmt fixture is fetched first
# so TestE2E_Fidelity_Fmt_SymbolEquivalent doesn't self-skip.
e2e-fidelity-fmt: check-tools converter fetch-fmt
	$(GO) test -tags=e2e -run TestE2E_Fidelity_Fmt ./orchestrator/...

# Real-Buildbarn validation. Brings up bb-storage via docker compose,
# runs the cache-share keystone test against grpc://127.0.0.1:8980,
# tears down. Replaces the in-process fake with actual Buildbarn code.
# Requires docker compose; no other toolchain bar.
BUILDBARN_COMPOSE := deploy/buildbarn/docker-compose.yml

buildbarn-up:
	docker compose -f $(BUILDBARN_COMPOSE) up -d
	@echo "waiting for bb-storage healthcheck..."
	@for i in $$(seq 1 180); do \
		if curl -fsS http://127.0.0.1:9980/-/healthy >/dev/null 2>&1; then echo "ready in $${i}s"; exit 0; fi; \
		sleep 1; \
	done; \
	echo "bb-storage did not become healthy within 180s; container logs:"; \
	docker compose -f $(BUILDBARN_COMPOSE) ps; \
	docker compose -f $(BUILDBARN_COMPOSE) logs --no-color --timestamps --tail=200 bb-storage; \
	exit 1

buildbarn-down:
	docker compose -f $(BUILDBARN_COMPOSE) down -v

e2e-buildbarn: buildbarn-up
	$(GO) test -tags=buildbarn -run TestE2E_Buildbarn ./orchestrator/...
	$(MAKE) buildbarn-down

# M3b execution validation against real Buildbarn workers (scheduler +
# bb-worker + bb-runner-bare from the same docker-compose stack).
# Runs both the synthetic /bin/sh round-trip test and the real-converter
# test (which depends on the custom worker image at
# deploy/buildbarn/runner/Dockerfile having cmake/ninja/bwrap installed).
# `make converter` is a prerequisite of the real-converter test; if the
# binary isn't present that subtest skips with a clear message.
e2e-buildbarn-execute: converter buildbarn-up
	$(GO) test -tags=buildbarn -run TestE2E_Buildbarn_Execute ./internal/reapi/...
	$(MAKE) buildbarn-down

# Fetch the M2 acceptance package out-of-band. Idempotent.
fetch-fmt:
	@if [ ! -d "$(FMT_DIR)" ]; then \
		git clone --depth 1 --branch $(FMT_VERSION) https://github.com/fmtlib/fmt.git "$(FMT_DIR)"; \
	else \
		echo "fmt already at $(FMT_DIR); rm -rf to refetch"; \
	fi

# Regenerate golden files. Re-runs the pipeline, overwrites *.golden.
update-golden:
	$(GO) test ./... -update

# Re-run cmake on each sample project, capture File API reply dirs into testdata.
record-fixtures: check-tools
	./tools/fixtures/record-fileapi.sh

lint: vet fmt

vet:
	$(GO) vet ./...

fmt:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then \
		echo "gofmt diffs in:"; echo "$$out"; \
		echo "run 'gofmt -w .'"; exit 1; \
	fi

check-tools:
	@command -v cmake >/dev/null || (echo "cmake not on PATH"; exit 1)
	@command -v ninja >/dev/null || (echo "ninja not on PATH"; exit 1)
	@command -v bwrap >/dev/null || (echo "bwrap not on PATH (apt install bubblewrap)"; exit 1)
	@cmake --version | head -1
	@ninja --version
	@bwrap --version 2>&1 | head -1

clean:
	rm -rf $(BUILD_DIR)
