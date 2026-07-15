# Makefile -- the single task entry point, shared by CI and local runs (docs/design/engineering.md §1)
#
# Naming convention (uniform shape across P1 / P3 / P4 / future P5 build variants):
#   make <verb>          = same semantics as <verb>-all (run all variants)
#   make <verb>-all      = run all variants (explicit)
#   make <verb>-p1       = P1 only (default build, crescent interpreter, P3/P4 dead code)
#   make <verb>-p3       = P3 only (wangshu_p3+wangshu_profile, P1 interpreter + P3 gibbous wasm tier)
#   make <verb>-p4       = P4 only (wangshu_p4, P1 interpreter + P4 gibbous jit tier; at the PJ0 stage
#                          supported is all-false => behaviorally equivalent to P1)
#   (a future P5 extends the same -p5 pattern, aggregated into -all)
#
# **P3+P4 build tags are mutually exclusive** (user decision, 06-backends.md §1):
# wangshu_p3 and wangshu_p4 must not be enabled together -- build-test-bins.sh
# rejects that combination.
#
# Verbs covered: build / test / bench / fuzz / difftest / conformance
# (test/bench go through precompiled .test binaries; fuzz/difftest/conformance
#  stay on the native `go test` path -- the fuzz corpus depends on the source
#  tree, difftest depends on the external lua5.1 oracle, and conformance is
#  small and stable enough that binary mode is not worth it)
.PHONY: all fmt lint \
        build build-all build-p1 build-p3 build-p4 build-clean \
        test test-all test-p1 test-p3 test-p4 test-trace \
        bench bench-all bench-p1 bench-p3 bench-p4 bench-test bench-pineapple bench-pineapple-fetch \
        fuzz fuzz-all fuzz-p1 fuzz-p3 fuzz-p4 fuzz-oracle \
        difftest difftest-all difftest-p1 difftest-p3 difftest-p4 \
        conformance conformance-all conformance-p1 conformance-p3 conformance-p4 \
        cover hooks check-pr-ci tidy release

all: fmt lint build-all test-all fuzz-all conformance difftest-all      ## Default: full local pre-commit check (main module + benchmarks submodule); build-all compiles the .test binaries for all variants once and later test/bench reuse them; the benchmarks submodule functional tests (realworld oracle parity) are already covered by test-all via precompiled binaries, so bench-test is not duplicated in all

fmt:                                                ## Format (writes back)
	@files=$$(git ls-files '*.go'); [ -z "$$files" ] || gofmt -w $$files

lint:                                               ## Repo-wide static checks
	golangci-lint run ./...

# --- build ------------------------------------------------------------------
build: build-all                                    ## Alias: make build = build-all

build-all: build-p1 build-p3 build-p4                ## Build .test binaries for all variants

build-p1:                                           ## Build P1 .test binaries into test-bin/p1/ (default build)
	./scripts/build-test-bins.sh p1

build-p3:                                           ## Build P3 .test binaries into test-bin/p3/ (wangshu_p3+wangshu_profile)
	./scripts/build-test-bins.sh p3

build-p4:                                           ## Build P4 .test binaries into test-bin/p4/ (wangshu_p4; at the PJ0 package-skeleton stage behavior is equivalent to P1)
	./scripts/build-test-bins.sh p4

build-clean:                                        ## Wipe test-bin/
	rm -rf test-bin

# --- test -------------------------------------------------------------------
test: test-all                                      ## Alias: make test = test-all

test-all: build-all                                 ## Run unit tests for all variants (precompiled binaries from test-bin/)
	./scripts/run-test-bins.sh test

test-p1: build-p1                                   ## Run unit tests for the P1 variant only
	./scripts/run-test-bins.sh test p1

test-p3: build-p3                                   ## Run unit tests for the P3 variant only
	./scripts/run-test-bins.sh test p3

test-p4: build-p4                                   ## Run unit tests for the P4 variant only (PJ0-PJ4 + PJ7 + PJ10 delivered: LOADK/MOVE/arith/compare/UNM/LEN/NOT/NEWTABLE/GETTABLE/SETTABLE/SELF/FORLOOP wired in + byte-level inline for all six IC paths; test/difftest/p4_test.go uses force-all + repeated calls + a PromotionCount>0 guard)
	./scripts/run-test-bins.sh test p4

test-trace:                                         ## Main-module unit tests (wangshu_trace build: verifyCISeg and other trace-gated safety nets active), native go test path
	go test -race -tags wangshu_trace ./...

bench-test:                                         ## Functional tests of the benchmarks submodule (realworld oracle parity), native go test path -- no longer part of make all (test-all covers it via precompiled binaries), kept as a standalone target
	cd benchmarks && go test -race ./...

cover:                                              ## Coverage (coverage.out + terminal summary), native go test path (merging coverage profiles is complex, so no binary mode)
	go test -race -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -func=coverage.out | tail -1

# --- fuzz (native go test path -- the fuzz corpus depends on the source tree) ---
fuzz: fuzz-all                                      ## Alias: make fuzz = fuzz-all

fuzz-all: fuzz-p1 fuzz-p3 fuzz-p4                   ## Run the fuzz smoke for all variants

fuzz-p1:                                            ## One smoke round of every fuzz target under the P1 build (default build)
	./scripts/go-fuzz.sh 30s

fuzz-p3:                                            ## One smoke round of every fuzz target under the P3 build (wangshu_p3+wangshu_profile, force-all promoted paths)
	./scripts/go-fuzz.sh 30s "wangshu_p3 wangshu_profile"

fuzz-p4:                                            ## One smoke round of every fuzz target under the P4 build (wangshu_p4; at the PJ0 stage equivalent to the P1 path)
	./scripts/go-fuzz.sh 30s "wangshu_p4 wangshu_profile"

fuzz-oracle:                                        ## In-process differential fuzz against the cgo-embedded official 5.1.5 (needs a local gcc; not part of all; shim unit tests + FuzzOracleDiff smoke)
	CGO_ENABLED=1 go test -tags wangshu_oracle_cgo ./internal/oracle/ -count=1
	@# Required-target assertion (PR review): the smoke must fail loudly
	@# if FuzzOracleDiff stops compiling into the root package under the
	@# oracle tags, instead of go-fuzz.sh skipping it while other
	@# targets keep the run green.
	CGO_ENABLED=1 go test -tags wangshu_oracle_cgo . -run='^$$' -list '^FuzzOracleDiff$$' | grep -q '^FuzzOracleDiff$$' \
		|| { echo "fuzz-oracle: FuzzOracleDiff missing under wangshu_oracle_cgo (build tag broken?)" >&2; exit 1; }
	CGO_ENABLED=1 ./scripts/go-fuzz.sh 30s "wangshu_oracle_cgo"

# --- conformance --------------------------------------------------------------
conformance: conformance-all                        ## Alias: make conformance = conformance-all

conformance-all: conformance-p1 conformance-p3 conformance-p4   ## Run the conformance suite for all variants (83 corner cases + large SETLIST tables)

conformance-p1:                                     ## P1 build conformance (default build, crescent interpreter path)
	go test ./test/conformance/...

conformance-p3:                                     ## P3 build conformance (wangshu_p3+wangshu_profile; the harness sets SetForceAllPromote(true) explicitly, so the gibbous wasm execution path is really exercised)
	go test -tags "wangshu_p3 wangshu_profile" ./test/conformance/...

conformance-p4:                                     ## P4 build conformance (wangshu_p4; the SupportsAllOpcodes whitelist covers ~25 shape classes + all six IC paths, but conformance cases are mostly small one-shot scripts and ~91% never reach the P4 promotion gate -- force-all is nominally on yet actual P4 path coverage is limited; real P4 path acceptance is difftest-p4)
	go test -tags "wangshu_p4 wangshu_profile" ./test/conformance/...

# --- difftest (native go test path -- depends on the external lua5.1 oracle) ---
difftest: difftest-all                              ## Alias: make difftest = difftest-all

difftest-all: difftest-p1 difftest-p3 difftest-p4   ## Run the differential fuzz for all variants

difftest-p1:                                        ## P1 build differential fuzz (default build vs official 5.1.5; 12 §3; needs a local lua5.1)
	./scripts/check-oracle.sh
	go test ./test/difftest/... -count=1

difftest-p3:                                        ## P3 build differential fuzz (force-all promoted gibbous wasm output vs gopher/official, V1-V13 three-way byte-equal)
	./scripts/check-oracle.sh
	go test -tags "wangshu_p3 wangshu_profile" ./test/difftest/... -count=1

difftest-p4:                                        ## P4 build differential fuzz (wangshu_p4; the P4-specific harness in test/difftest/p4_test.go: force-all + 17 p4Corpus cases with repeated calls per kernel + a PromotionCount>0 backstop so the P4 native path is really reached at the difftest level; crescent / p4-jit three-way byte-equal)
	./scripts/check-oracle.sh
	go test -tags "wangshu_p4 wangshu_profile" ./test/difftest/... -count=1

# --- bench --------------------------------------------------------------------
bench: bench-all                                    ## Alias: make bench = bench-all

bench-all: build-all                                ## Run every benchmark for all variants (main + benchmarks submodule)
	./scripts/run-test-bins.sh bench

bench-pineapple:                                    ## Run the wangshu-as-pineapple-backend three-way comparison benchmark (run make bench-pineapple-fetch first)
	$(MAKE) -C benchmarks/pineapple bench

bench-pineapple-fetch:                              ## Clone/update pineapple master into benchmarks/pineapple/.pineapple/
	$(MAKE) -C benchmarks/pineapple fetch

bench-p1: build-p1                                  ## Run benchmarks for the P1 variant only
	./scripts/run-test-bins.sh bench p1

bench-p3: build-p3                                  ## Run benchmarks for the P3 variant only
	./scripts/run-test-bins.sh bench p3

bench-p4: build-p4                                  ## Run benchmarks for the P4 variant only (PJ0 stage: behaviorally equivalent to P1)
	./scripts/run-test-bins.sh bench p4

# --- misc ---------------------------------------------------------------------
hooks:                                              ## Install git hooks (one-time)
	git config core.hooksPath .githooks
	@echo "hooks installed: $$(git config core.hooksPath)"

check-pr-ci:                                        ## Manual trigger: block until the PR CI finishes + collect post-push review activity (same script the pre-push self-wrapper calls); exits 2 on a fresh branch with no PR yet
	./scripts/check-pr-ci.sh

tidy:                                               ## go mod tidy for the main module + the benchmarks submodule
	go mod tidy
	cd benchmarks && go mod tidy
	git diff --exit-code go.mod go.sum benchmarks/go.mod benchmarks/go.sum

release:                                            ## Create an annotated tag (no push): make release TAG=vX.Y.Z [MESSAGE='single-line notes' | MESSAGE_FILE=path/to/notes.txt]
	@if [ -z "$(TAG)" ]; then \
		echo "ERROR: TAG is required."; \
		echo "Usage: make release TAG=v0.2.0-rc3 [MESSAGE='release notes' | MESSAGE_FILE=path/to/notes.txt]"; \
		exit 1; \
	fi
	@if [ -n "$(MESSAGE)" ] && [ -n "$(MESSAGE_FILE)" ]; then \
		echo "ERROR: MESSAGE and MESSAGE_FILE are mutually exclusive."; \
		exit 1; \
	fi
	@if git rev-parse --verify --quiet "refs/tags/$(TAG)" >/dev/null; then \
		echo "ERROR: tag $(TAG) already exists at $$(git rev-parse $(TAG))"; \
		exit 1; \
	fi
	@if [ -n "$(MESSAGE_FILE)" ]; then \
		if [ ! -f "$(MESSAGE_FILE)" ]; then \
			echo "ERROR: MESSAGE_FILE not found: $(MESSAGE_FILE)"; \
			exit 1; \
		fi; \
		git tag -a "$(TAG)" -F "$(MESSAGE_FILE)"; \
	elif [ -n "$(MESSAGE)" ]; then \
		git tag -a "$(TAG)" -m "$(MESSAGE)"; \
	else \
		git tag -a "$(TAG)" -m "Release $(TAG)"; \
	fi
	@echo "tagged $(TAG) on $$(git rev-parse HEAD)"
	@echo "push to remote with: git push origin $(TAG)"
