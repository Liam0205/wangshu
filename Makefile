# Makefile —— 唯一任务入口,CI 与本地共用(docs/design/engineering.md §1)
#
# 命名约定(P1 / P3 / P4 / 未来 P5 等多 build variant 统一形态):
#   make <verb>          = <verb>-all 的语义(跑所有 variant)
#   make <verb>-all      = 跑全部 variant(显式)
#   make <verb>-p1       = 只跑 P1(默认 build,新月解释器,P3/P4 dead-code)
#   make <verb>-p3       = 只跑 P3(wangshu_p3+wangshu_profile,P1 解释器+P3 凸月 wasm 编译层)
#   make <verb>-p4       = 只跑 P4(wangshu_p4,P1 解释器+P4 凸月 jit 编译层;PJ0 阶段:supported 全 false ⇒ 行为等价 P1)
#   (未来追加 P5 时同款扩 -p5,with -all 顺手聚合)
#
# **P3+P4 互斥 build tag**(用户裁决,06-backends.md §1):wangshu_p3 与
# wangshu_p4 不允许同时启用——build-test-bins.sh 拒此组合。
#
# 涉及 verb:build / test / bench / fuzz / difftest / conformance
# (test/bench 走预编译 .test binary 流;fuzz/difftest/conformance 留 `go test`
#  原生路径——fuzz corpus 依赖 source tree、difftest 依赖外部 lua5.1 oracle、
#  conformance 小且稳定不易/不必 binary 化)
.PHONY: all fmt lint \
        build build-all build-p1 build-p3 build-p4 build-clean \
        test test-all test-p1 test-p3 test-p4 test-trace \
        bench bench-all bench-p1 bench-p3 bench-p4 bench-test bench-pineapple bench-pineapple-fetch \
        fuzz fuzz-all fuzz-p1 fuzz-p3 fuzz-p4 fuzz-oracle \
        difftest difftest-all difftest-p1 difftest-p3 difftest-p4 \
        conformance conformance-all conformance-p1 conformance-p3 conformance-p4 \
        cover hooks check-pr-ci tidy release

all: fmt lint build-all test-all fuzz-all conformance difftest-all      ## 默认:提交前本地全检(主模块 + benchmarks 子模块);build-all 一次编两 variant 的 .test binary,后续 test/bench 复用;benchmarks 子模块功能测试(realworld oracle parity)已由 test-all 跑预编 binary 覆盖,bench-test 不重复挂在 all 里

fmt:                                                ## 格式化(写回)
	@files=$$(git ls-files '*.go'); [ -z "$$files" ] || gofmt -w $$files

lint:                                               ## 全仓静态检查
	golangci-lint run ./...

# ─── build ─────────────────────────────────────────────────────────────────
build: build-all                                    ## 别名:make build = build-all

build-all: build-p1 build-p3 build-p4                ## 编全部 variant 的 .test binary

build-p1:                                           ## 编 P1 .test binary 到 test-bin/p1/(默认 build)
	./scripts/build-test-bins.sh p1

build-p3:                                           ## 编 P3 .test binary 到 test-bin/p3/(wangshu_p3+wangshu_profile)
	./scripts/build-test-bins.sh p3

build-p4:                                           ## 编 P4 .test binary 到 test-bin/p4/(wangshu_p4,PJ0 包骨架阶段:与 P1 行为等价)
	./scripts/build-test-bins.sh p4

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

test-p4: build-p4                                   ## 只跑 P4 variant 的单测(PJ0-PJ4 + PJ7 + PJ10 已落地:LOADK/MOVE/算术/比较/UNM/LEN/NOT/NEWTABLE/GETTABLE/SETTABLE/SELF/FORLOOP 真接入 + IC 六路径字节级 inline;test/difftest/p4_test.go 强制 force-all + 重复调用 + PromotionCount>0 守卫)
	./scripts/run-test-bins.sh test p4

test-trace:                                         ## 主模块单测(wangshu_trace build:verifyCISeg 等 trace-gated 安全网激活)走原生 go test 路径
	go test -race -tags wangshu_trace ./...

bench-test:                                         ## benchmarks 子模块的功能测试(realworld oracle parity)走原生 go test 路径——已不在 make all 里(test-all 经预编 binary 已覆盖),保留作 standalone target
	cd benchmarks && go test -race ./...

cover:                                              ## 覆盖率(coverage.out + 终端摘要)走原生 go test 路径(coverage profile 合并复杂,不上 binary 模式)
	go test -race -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -func=coverage.out | tail -1

# ─── fuzz(走 go test 原生路径——fuzz corpus 依赖 source tree)──────────────
fuzz: fuzz-all                                      ## 别名:make fuzz = fuzz-all

fuzz-all: fuzz-p1 fuzz-p3 fuzz-p4                   ## 跑全部 variant 的 fuzz 冒烟

fuzz-p1:                                            ## P1 build 下全部 fuzz 目标各跑一轮冒烟(默认 build)
	./scripts/go-fuzz.sh 30s

fuzz-p3:                                            ## P3 build 下全部 fuzz 目标各跑一轮冒烟(wangshu_p3+wangshu_profile,force-all 升层后路径)
	./scripts/go-fuzz.sh 30s "wangshu_p3 wangshu_profile"

fuzz-p4:                                            ## P4 build 下全部 fuzz 目标各跑一轮冒烟(wangshu_p4,PJ0 阶段与 P1 等价路径)
	./scripts/go-fuzz.sh 30s "wangshu_p4 wangshu_profile"

fuzz-oracle:                                        ## cgo 内嵌官方 5.1.5 进程内差分 fuzz(需本机 gcc;不进 all——唯一 cgo 面,shim 单测 + FuzzOracleDiff 冒烟)
	CGO_ENABLED=1 go test -tags wangshu_oracle_cgo ./internal/oracle/ -count=1
	CGO_ENABLED=1 ./scripts/go-fuzz.sh 30s "wangshu_oracle_cgo"

# ─── conformance ───────────────────────────────────────────────────────────
conformance: conformance-all                        ## 别名:make conformance = conformance-all

conformance-all: conformance-p1 conformance-p3 conformance-p4   ## 跑全部 variant 的 conformance 套(83 角落用例 + SETLIST 大表)

conformance-p1:                                     ## P1 build conformance(默认 build,新月解释器路径)
	go test ./test/conformance/...

conformance-p3:                                     ## P3 build conformance(wangshu_p3+wangshu_profile,harness 显式 SetForceAllPromote(true) → 真走凸月 wasm 执行路径)
	go test -tags "wangshu_p3 wangshu_profile" ./test/conformance/...

conformance-p4:                                     ## P4 build conformance(wangshu_p4;SupportsAllOpcodes 白名单已扩 ~25 类形态 + IC 六路径,但 conformance 用例多为单次小脚本,~91% 不达 P4 升层闸门——force-all 形式上启用但实际 P4 路径覆盖受限,真 P4 路径验收以 difftest-p4 为准)
	go test -tags "wangshu_p4 wangshu_profile" ./test/conformance/...

# ─── difftest(走 go test 原生路径——依赖外部 lua5.1 oracle)─────────────────
difftest: difftest-all                              ## 别名:make difftest = difftest-all

difftest-all: difftest-p1 difftest-p3 difftest-p4   ## 跑全部 variant 的差分 fuzz

difftest-p1:                                        ## P1 build 差分 fuzz(默认 build vs 官方 5.1.5;12 §3;需本机 lua5.1)
	./scripts/check-oracle.sh
	go test ./test/difftest/... -count=1

difftest-p3:                                        ## P3 build 差分 fuzz(force-all 升层后凸月 wasm 输出 vs gopher/官方,V1-V13 三方逐字节)
	./scripts/check-oracle.sh
	go test -tags "wangshu_p3 wangshu_profile" ./test/difftest/... -count=1

difftest-p4:                                        ## P4 build 差分 fuzz(wangshu_p4;test/difftest/p4_test.go P4 专属 harness:force-all + p4Corpus 17 用例每核重复调用 + PromotionCount>0 兜底,确保 P4 native 路径在 difftest 整套层面真触达;crescent / p4-jit 三方对拍 byte-equal)
	./scripts/check-oracle.sh
	go test -tags "wangshu_p4 wangshu_profile" ./test/difftest/... -count=1

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

bench-p4: build-p4                                  ## 只跑 P4 variant 的 benchmark(PJ0 阶段:行为等价 P1)
	./scripts/run-test-bins.sh bench p4

# ─── 其他 ──────────────────────────────────────────────────────────────────
hooks:                                              ## 安装 git hooks(一次性)
	git config core.hooksPath .githooks
	@echo "hooks installed: $$(git config core.hooksPath)"

check-pr-ci:                                        ## 手动 trigger:阻塞等 PR CI 跑完 + 抓 push 后新 review 活动(同 pre-push self-wrapper 内部调的);新分支首推无 PR 时 exit 2
	./scripts/check-pr-ci.sh

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
