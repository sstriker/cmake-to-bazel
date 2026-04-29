.PHONY: all converter orchestrator diff history bst-translate derive-toolchain test test-e2e e2e-hello-world e2e-fmt \
        e2e-orchestrate e2e-orchestrate-scale e2e-bazel-build e2e-cmake-consumer e2e-toolchain-skip e2e-fidelity e2e-fidelity-fmt e2e-buildbarn e2e-buildbarn-execute \
        e2e-meta-hello e2e-meta-stack \
        buildbarn-up buildbarn-down install-bazelisk install-cmake convert-and-build \
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

# Phase 1 acceptance gate for the meta-project (Bazel-as-orchestrator)
# shape (docs/whole-project-plan.md). Renders project A and project B
# from the hello-world fixture via cmd/write-a, then drives the full
# two-pass pipeline: bazel build A (runs convert-element via genrule)
# -> stage A's BUILD.bazel.out into B -> bazel build + run B's smoke
# binary linking against the converted cc_library. Skips the bazel
# phases cleanly when bazel >= 7 isn't on PATH; the rendering checks
# alone are still a useful regression gate.
e2e-meta-hello: check-tools converter
	scripts/meta-hello.sh

# Phase 2 acceptance gate for the meta-project. Multi-element fixture
# (testdata/meta-project/two-libs/) — two kind:cmake elements + one
# kind:stack bundling them. Validates write-a's per-kind dispatch +
# graph-shape rendering + the kind:stack handler's filegroup
# composition end-to-end through both projects, with a smoke binary
# linking against both cmake elements. Same bazel-availability
# gating as e2e-meta-hello.
e2e-meta-stack: check-tools converter
	scripts/meta-stack.sh

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
	@docker compose -f $(BUILDBARN_COMPOSE) up -d || ( \
		echo "buildbarn: compose up failed; dumping container state + logs:"; \
		docker compose -f $(BUILDBARN_COMPOSE) ps; \
		docker compose -f $(BUILDBARN_COMPOSE) logs --no-color --timestamps --tail=200; \
		exit 1; \
	)
	@echo "waiting for bb-storage + bb-scheduler + bb-worker HTTP /-/healthy..."
	@# Each container exposes /-/healthy on its diagnostics http port:
	@#   bb-storage   :9980
	@#   bb-scheduler :9982
	@#   bb-worker    :9981
	@# Polling only bb-storage masked schema-config crashes elsewhere;
	@# polling only storage + scheduler still missed bb-worker
	@# crashes (TestE2E_Buildbarn_Execute then saw "No workers exist
	@# for instance name prefix ..." because bb-worker had never
	@# managed to register against bb-scheduler:8984).
	@#
	@# Waiting for all three closes the visibility gap. Worker health
	@# also serves as a coarse "scheduler has at least seen the
	@# worker" signal — bb-worker only binds its diagnostics http
	@# AFTER its config parses and BEFORE registering with the
	@# scheduler, so a successful poll guarantees the config is
	@# valid; registration follows shortly after on the same process.
	@for i in $$(seq 1 180); do \
		if curl -fsS http://127.0.0.1:9980/-/healthy >/dev/null 2>&1 \
		   && curl -fsS http://127.0.0.1:9982/-/healthy >/dev/null 2>&1 \
		   && curl -fsS http://127.0.0.1:9981/-/healthy >/dev/null 2>&1; then \
			echo "ready in $${i}s"; exit 0; \
		fi; \
		sleep 1; \
	done; \
	echo "buildbarn stack did not become healthy within 180s; container logs:"; \
	docker compose -f $(BUILDBARN_COMPOSE) ps; \
	docker compose -f $(BUILDBARN_COMPOSE) logs --no-color --timestamps --tail=200; \
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
	@# On test failure, dump per-container logs before tearing down so
	@# CI shows worker/scheduler/runner state when "No workers exist"
	@# or similar registration-side errors fire. /-/healthy says "the
	@# diagnostics http server bound" but says nothing about whether
	@# bb-worker has finished registering against bb-scheduler:8984;
	@# the logs disambiguate.
	@$(GO) test -tags=buildbarn -run TestE2E_Buildbarn_Execute ./internal/reapi/...; \
	  ec=$$?; \
	  if [ $$ec -ne 0 ]; then \
	    echo "=== container state ==="; \
	    docker compose -f $(BUILDBARN_COMPOSE) ps -a; \
	    echo "=== bb-runner-bare inspect (RestartCount, ExitCode, OOMKilled) ==="; \
	    docker inspect bb-runner-bare --format='RestartCount={{.RestartCount}} State.Status={{.State.Status}} ExitCode={{.State.ExitCode}} OOMKilled={{.State.OOMKilled}} Error="{{.State.Error}}"' || true; \
	    echo "=== bb-runner-bare healthcheck history ==="; \
	    docker inspect bb-runner-bare --format='{{range .State.Health.Log}}{{.Start}} ExitCode={{.ExitCode}} Output={{.Output | printf "%q"}}{{println}}{{end}}' || true; \
	    echo "=== bb-runner-bare procs ==="; \
	    docker top bb-runner-bare || true; \
	    echo "=== bb-runner-bare find for runner.sock anywhere ==="; \
	    docker compose -f $(BUILDBARN_COMPOSE) exec -T bb-runner-bare find / -name 'runner.sock' -o -name 'runner' -type s 2>/dev/null || true; \
	    echo "=== bb-runner-bare /worker tree ==="; \
	    docker compose -f $(BUILDBARN_COMPOSE) exec -T bb-runner-bare ls -lR /worker 2>&1 || true; \
	    echo "=== bb-runner-bare /tmp ==="; \
	    docker compose -f $(BUILDBARN_COMPOSE) exec -T bb-runner-bare ls -la /tmp 2>&1 || true; \
	    for svc in bb-worker bb-scheduler bb-runner-bare bb-storage; do \
	      echo "=== $$svc logs ==="; \
	      docker compose -f $(BUILDBARN_COMPOSE) logs --no-color --timestamps --tail=100 $$svc; \
	    done; \
	    echo "=== bb-runner-bare /worker/build view ==="; \
	    docker compose -f $(BUILDBARN_COMPOSE) exec -T bb-runner-bare ls -la /worker/build/ || true; \
	    echo "=== bb-worker /worker/build view ==="; \
	    docker compose -f $(BUILDBARN_COMPOSE) exec -T bb-worker ls -la /worker/build/ || true; \
	    echo "=== bb-worker uid/gid ==="; \
	    docker compose -f $(BUILDBARN_COMPOSE) exec -T bb-worker id || true; \
	    echo "=== bb-runner-bare uid/gid ==="; \
	    docker compose -f $(BUILDBARN_COMPOSE) exec -T bb-runner-bare id || true; \
	  fi; \
	  $(MAKE) buildbarn-down; \
	  exit $$ec

# Local-dev bazelisk bootstrap. Installs to ~/.local/bin by default
# (override with PREFIX=). The bazel-tagged e2e tests self-skip when
# bazel is missing; this gets them out of skip-mode without operators
# having to read README footnotes.
install-bazelisk:
	tools/install-bazelisk.sh

# Local-dev pinned-cmake bootstrap. Same pin CI installs and the
# worker image ships (CMAKE_VERSION above). Use this when your distro
# ships a different cmake than what defaultPlatform asserts; otherwise
# converter behavior on a newer cmake (e.g. ubuntu-24.04's 3.31.6)
# slips past local dev and only fires in CI.
install-cmake:
	tools/install-pinned-cmake.sh

# Full pipeline: convert an FDSDK-style root, then bazel build inside
# the resulting <out>/. Requires bazel or bazelisk on PATH (run
# `make install-bazelisk` first if missing). Override BAZEL_TARGET to
# build a specific label instead of //... .
#
#   make convert-and-build FDSDK_ROOT=/path/to/fdsdk OUT=/tmp/out
#   make convert-and-build FDSDK_ROOT=/path/to/fdsdk OUT=/tmp/out BAZEL_TARGET=//elements/components/foo:bar
convert-and-build: converter orchestrator
	@[ -n "$(FDSDK_ROOT)" ] || (echo "set FDSDK_ROOT=path/to/fdsdk-root"; exit 1)
	@[ -n "$(OUT)" ] || (echo "set OUT=output-dir"; exit 1)
	$(ORCHESTRATOR) --fdsdk-root $(FDSDK_ROOT) --out $(OUT) --converter $(CONVERTER) --bazel-build $(or $(BAZEL_TARGET),//...)

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
	@cmake --version | head -1
	@ninja --version

clean:
	rm -rf $(BUILD_DIR)
