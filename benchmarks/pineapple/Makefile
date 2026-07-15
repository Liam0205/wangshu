# benchmarks/pineapple/Makefile -- local Make entry point for the submodule
.PHONY: fetch bench bench-p1 bench-p3 bench-gopher tidy

fetch:                                          ## Clone/update pineapple master into .pineapple/
	./scripts/fetch-pineapple.sh

bench: bench-p1 bench-p3 bench-gopher           ## Run benchmarks under all three builds (P1 + P3Auto + Gopher three-way comparison)

bench-p1: fetch                                 ## wangshu p1 build (default wangshu backend)
	@echo "===== WangshuP1(默认 wangshu backend)====="
	go test -bench=. -benchmem -count=3 -run='^$$' .

bench-p3: fetch                                 ## wangshu p3 build (natural-heat promotion)
	@echo "===== WangshuP3Auto(p3+profile,自然热度升层)====="
	go test -tags "wangshu_p3 wangshu_profile" -bench=. -benchmem -count=3 -run='^$$' .

bench-gopher: fetch                             ## pineapple switched to the gopher-lua backend
	@echo "===== Gopher(pineapple 切 gopher-lua backend)====="
	go test -tags lua_gopher -bench=. -benchmem -count=3 -run='^$$' .

tidy: fetch                                     ## go mod tidy (needs .pineapple/ present)
	go mod tidy
