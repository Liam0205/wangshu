# Makefile —— 唯一任务入口,CI 与本地共用(docs/design/engineering.md §1)
.PHONY: all fmt lint test bench-test race cover fuzz bench bench-gibbous bench-all conformance difftest hooks tidy

all: fmt lint test fuzz conformance difftest bench-test      ## 默认:提交前本地全检(主模块 + benchmarks 子模块)

fmt:                                                ## 格式化(写回)
	@files=$$(git ls-files '*.go'); [ -z "$$files" ] || gofmt -w $$files

lint:                                               ## 全仓静态检查
	golangci-lint run ./...

test:                                               ## 主模块单测(含 race)
	go test -race ./...

bench-test:                                         ## benchmarks 子模块的测试(realworld oracle parity)
	cd benchmarks && go test -race ./...

cover:                                              ## 覆盖率(coverage.out + 终端摘要)
	go test -race -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -func=coverage.out | tail -1

fuzz:                                               ## 全部 fuzz 目标各跑一轮冒烟(自动发现 func Fuzz*)
	./scripts/go-fuzz.sh 30s

conformance:                                        ## conformance 套(12 §2)
	go test ./test/conformance/...

difftest:                                           ## 一轮固定时长差分 fuzz(12 §3;需本机 lua5.1)
	./scripts/check-oracle.sh
	go test ./test/difftest/... -count=1

bench:                                              ## 四档基准:纯VM micro(baseline)+ 真实负载纯VM(realworld)+ 边界 mini(embedded/Mini)+ 真实负载 embedded(embedded/Realworld);benchmarks 独立子模块,gopher-lua 仅基准用不污染主模块依赖图
	cd benchmarks && go test -bench=. -benchmem -count=1 -run='^$$' ./...

bench-gibbous:                                      ## 凸月(gibbous)档基准(需 wangshu_p3+profile;force-all 升 wazero)
	cd benchmarks && go test -tags "wangshu_p3 wangshu_profile" -bench=Gibbous -benchmem -count=1 -run='^$$' ./...

bench-all:                                           ## 大一统:一条命令出 gopher/新月/凸月三方表。注意分两段跑——新月/gopher 用默认 tag(无 profiling 税,反映真实 baseline);凸月用 p3+profile tag(force-all 需采样钩)。profiling 会给新月加 ~28% 税,故不混跑同一 tag。
	@echo "===== 新月(crescent)+ gopher:默认 tag(无 profiling 税)====="
	cd benchmarks && go test -bench=. -benchmem -count=1 -run='^$$' ./...
	@echo "===== 凸月(gibbous):wangshu_p3 wangshu_profile + force-all ====="
	cd benchmarks && go test -tags "wangshu_p3 wangshu_profile" -bench=Gibbous -benchmem -count=1 -run='^$$' ./...

hooks:                                              ## 安装 git hooks(一次性)
	git config core.hooksPath .githooks
	@echo "hooks installed: $$(git config core.hooksPath)"

tidy:                                               ## 主模块 + benchmarks 子模块的 go mod tidy
	go mod tidy
	cd benchmarks && go mod tidy
	git diff --exit-code go.mod go.sum benchmarks/go.mod benchmarks/go.sum
