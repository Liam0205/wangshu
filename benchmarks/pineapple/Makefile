# benchmarks/pineapple/Makefile —— 子模块本地 Make 入口
.PHONY: fetch bench bench-p1 bench-p3 bench-gopher tidy

fetch:                                          ## clone/update pineapple master 到 .pineapple/
	./scripts/fetch-pineapple.sh

bench: bench-p1 bench-p3 bench-gopher           ## 跑全三 build 的 benchmark(P1 + P3Auto + Gopher 三路对照)

bench-p1: fetch                                 ## wangshu p1 build(默认 wangshu backend)
	@echo "===== WangshuP1(默认 wangshu backend)====="
	go test -bench=. -benchmem -count=3 -run='^$$' .

bench-p3: fetch                                 ## wangshu p3 build(自然热度升层)
	@echo "===== WangshuP3Auto(p3+profile,自然热度升层)====="
	go test -tags "wangshu_p3 wangshu_profile" -bench=. -benchmem -count=3 -run='^$$' .

bench-gopher: fetch                             ## pineapple 切 gopher-lua backend
	@echo "===== Gopher(pineapple 切 gopher-lua backend)====="
	go test -tags lua_gopher -bench=. -benchmem -count=3 -run='^$$' .

tidy: fetch                                     ## go mod tidy(需 .pineapple/ 存在)
	go mod tidy
