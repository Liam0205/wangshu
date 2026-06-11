# Makefile —— 唯一任务入口,CI 与本地共用(docs/design/engineering.md §1)
.PHONY: all fmt lint test race cover fuzz bench conformance difftest hooks tidy

all: fmt lint test                                  ## 默认:提交前本地全检

fmt:                                                ## 格式化(写回)
	@files=$$(git ls-files '*.go'); [ -z "$$files" ] || gofmt -w $$files

lint:                                               ## 全仓静态检查
	golangci-lint run ./...

test:                                               ## 全部单测(含 race)
	go test -race ./...

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

bench:                                              ## 三档基准(12 §6)
	go test -bench=. -benchmem -count=1 -run='^$$' ./benchmarks/...

hooks:                                              ## 安装 git hooks(一次性)
	git config core.hooksPath .githooks
	@echo "hooks installed: $$(git config core.hooksPath)"

tidy:
	go mod tidy && git diff --exit-code go.mod
