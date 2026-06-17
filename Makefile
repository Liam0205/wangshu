# Makefile —— 唯一任务入口,CI 与本地共用(docs/design/engineering.md §1)
#
# 命名约定(P1 / P3 / 未来 P4 / P5 等多 build variant 统一形态):
#   make <verb>          = <verb>-all 的语义(跑所有 variant)
#   make <verb>-all      = 跑全部 variant(显式)
#   make <verb>-p1       = 只跑 P1(默认 build,新月解释器,P3 dead-code)
#   make <verb>-p3       = 只跑 P3(wangshu_p3+wangshu_profile,P1 解释器+P3 凸月编译层)
#   (未来追加 P4 / P5 时同款扩 -p4 / -p5,with -all 顺手聚合)
#
# 涉及 verb:build / test / bench / fuzz / difftest / conformance
# (test/bench 走预编译 .test binary 流;fuzz/difftest/conformance 留 `go test`
#  原生路径——fuzz corpus 依赖 source tree、difftest 依赖外部 lua5.1 oracle、
#  conformance 小且稳定不易/不必 binary 化)
.PHONY: all fmt lint \
        build build-all build-p1 build-p3 build-clean \
        test test-all test-p1 test-p3 test-trace \
        bench bench-all bench-p1 bench-p3 bench-test bench-pineapple bench-pineapple-fetch \
        fuzz fuzz-all fuzz-p1 fuzz-p3 \
        difftest difftest-all difftest-p1 difftest-p3 \
        conformance conformance-all conformance-p1 conformance-p3 \
        cover hooks tidy release

all: fmt lint build-all test-all fuzz-all conformance difftest-all      ## 默认:提交前本地全检(主模块 + benchmarks 子模块);build-all 一次编两 variant 的 .test binary,后续 test/bench 复用;benchmarks 子模块功能测试(realworld oracle parity)已由 test-all 跑预编 binary 覆盖,bench-test 不重复挂在 all 里

fmt:                                                ## 格式化(写回)
	@files=$$(git ls-files '*.go'); [ -z "$$files" ] || gofmt -w $$files

lint:                                               ## 全仓静态检查
	golangci-lint run ./...

# ─── build ─────────────────────────────────────────────────────────────────
build: build-all                                    ## 别名:make build = build-all

build-all: build-p1 build-p3                        ## 编全部 variant 的 .test binary

build-p1:                                           ## 编 P1 .test binary 到 test-bin/p1/(默认 build)
	./scripts/build-test-bins.sh p1

build-p3:                                           ## 编 P3 .test binary 到 test-bin/p3/(wangshu_p3+wangshu_profile)
	./scripts/build-test-bins.sh p3

build-clean:                                        ## 清空 test-bin/
	rm -rf test-bin

# ─── test ──────────────────────────────────────────────────────────────────
test: test-all                                      ## 别名:make test = test-all

test-all: build-all                                 ## 跑全部 variant 的单测(从 test-bin/ 跑预编 binary)
	./scripts/run-test-bins.sh test

test-p1: build-p1                                   ## 只跑 P1 variant 的单测
	./scripts/run-test-bins.sh test p1

test-p3: build-p3                                   ## 只跑 P3 variant 的单测
	./scripts/run-test-bins.sh test p3

test-trace:                                         ## 主模块单测(wangshu_trace build:verifyCISeg 等 trace-gated 安全网激活)走原生 go test 路径
	go test -race -tags wangshu_trace ./...

bench-test:                                         ## benchmarks 子模块的功能测试(realworld oracle parity)走原生 go test 路径——已不在 make all 里(test-all 经预编 binary 已覆盖),保留作 standalone target
	cd benchmarks && go test -race ./...

cover:                                              ## 覆盖率(coverage.out + 终端摘要)走原生 go test 路径(coverage profile 合并复杂,不上 binary 模式)
	go test -race -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -func=coverage.out | tail -1

# ─── fuzz(走 go test 原生路径——fuzz corpus 依赖 source tree)──────────────
fuzz: fuzz-all                                      ## 别名:make fuzz = fuzz-all

fuzz-all: fuzz-p1 fuzz-p3                           ## 跑全部 variant 的 fuzz 冒烟

fuzz-p1:                                            ## P1 build 下全部 fuzz 目标各跑一轮冒烟(默认 build)
	./scripts/go-fuzz.sh 30s

fuzz-p3:                                            ## P3 build 下全部 fuzz 目标各跑一轮冒烟(wangshu_p3+wangshu_profile,force-all 升层后路径)
	./scripts/go-fuzz.sh 30s "wangshu_p3 wangshu_profile"

# ─── conformance ───────────────────────────────────────────────────────────
conformance: conformance-all                        ## 别名:make conformance = conformance-all

conformance-all: conformance-p1 conformance-p3      ## 跑全部 variant 的 conformance 套(83 角落用例 + SETLIST 大表)

conformance-p1:                                     ## P1 build conformance(默认 build,新月解释器路径)
	go test ./test/conformance/...

conformance-p3:                                     ## P3 build conformance(wangshu_p3+wangshu_profile,harness 显式 SetForceAllPromote(true) → 真走凸月 wasm 执行路径)
	go test -tags "wangshu_p3 wangshu_profile" ./test/conformance/...

# ─── difftest(走 go test 原生路径——依赖外部 lua5.1 oracle)─────────────────
difftest: difftest-all                              ## 别名:make difftest = difftest-all

difftest-all: difftest-p1 difftest-p3               ## 跑全部 variant 的差分 fuzz

difftest-p1:                                        ## P1 build 差分 fuzz(默认 build vs 官方 5.1.5;12 §3;需本机 lua5.1)
	./scripts/check-oracle.sh
	go test ./test/difftest/... -count=1

difftest-p3:                                        ## P3 build 差分 fuzz(force-all 升层后凸月 wasm 输出 vs gopher/官方,V1-V13 三方逐字节)
	./scripts/check-oracle.sh
	go test -tags "wangshu_p3 wangshu_profile" ./test/difftest/... -count=1

# ─── bench ─────────────────────────────────────────────────────────────────
bench: bench-all                                    ## 别名:make bench = bench-all

bench-all: build-all                                ## 跑全部 variant 的全部 benchmark(主+benchmarks 子模块)
	./scripts/run-test-bins.sh bench

bench-pineapple:                                    ## 跑 wangshu-as-pineapple-backend 三路对照 benchmark(需先 make bench-pineapple-fetch)
	$(MAKE) -C benchmarks/pineapple bench

bench-pineapple-fetch:                              ## clone/update pineapple master 到 benchmarks/pineapple/.pineapple/
	$(MAKE) -C benchmarks/pineapple fetch

bench-p1: build-p1                                  ## 只跑 P1 variant 的 benchmark
	./scripts/run-test-bins.sh bench p1

bench-p3: build-p3                                  ## 只跑 P3 variant 的 benchmark
	./scripts/run-test-bins.sh bench p3

# ─── 其他 ──────────────────────────────────────────────────────────────────
hooks:                                              ## 安装 git hooks(一次性)
	git config core.hooksPath .githooks
	@echo "hooks installed: $$(git config core.hooksPath)"

tidy:                                               ## 主模块 + benchmarks 子模块的 go mod tidy
	go mod tidy
	cd benchmarks && go mod tidy
	git diff --exit-code go.mod go.sum benchmarks/go.mod benchmarks/go.sum

release:                                            ## 打 annotated tag(本地不 push):make release TAG=vX.Y.Z [MESSAGE='single-line notes' | MESSAGE_FILE=path/to/notes.txt]
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
