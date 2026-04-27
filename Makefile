.PHONY: all converter orchestrator diff history bst-translate derive-toolchain test test-e2e e2e-hello-world e2e-fmt \
        e2e-orchestrate e2e-bazel-build e2e-cmake-consumer e2e-buildbarn e2e-buildbarn-execute \
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

# Real-Buildbarn validation. Brings up bb-storage via docker compose,
# runs the cache-share keystone test against grpc://127.0.0.1:8980,
# tears down. Replaces the in-process fake with actual Buildbarn code.
# Requires docker compose; no other toolchain bar.
BUILDBARN_COMPOSE := deploy/buildbarn/docker-compose.yml

buildbarn-up:
	docker compose -f $(BUILDBARN_COMPOSE) up -d
	@echo "waiting for bb-storage healthcheck..."
	@for i in $$(seq 1 60); do \
		if curl -fsS http://127.0.0.1:9980/-/healthy >/dev/null 2>&1; then echo "ready"; exit 0; fi; \
		sleep 1; \
	done; echo "bb-storage did not become healthy within 60s"; exit 1

buildbarn-down:
	docker compose -f $(BUILDBARN_COMPOSE) down -v

e2e-buildbarn: buildbarn-up
	$(GO) test -tags=buildbarn -run TestE2E_Buildbarn ./orchestrator/...
	$(MAKE) buildbarn-down

# M3b execution validation against real Buildbarn workers (scheduler +
# bb-worker + bb-runner-bare from the same docker-compose stack).
# Submits a synthetic /bin/sh action — does NOT run the converter
# end-to-end, since the bb-runner-bare image lacks cmake/ninja/bwrap.
# Closes the loop on the protocol round trip; full conversion needs
# a custom worker image (see deploy/buildbarn/README.md).
e2e-buildbarn-execute: buildbarn-up
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
