module github.com/Liam0205/wangshu/benchmarks/pineapple

go 1.26.2

// wangshu:本仓上级
replace github.com/Liam0205/wangshu => ../../

// pineapple:经 `make fetch` 拉到 .pineapple/(详见 scripts/fetch-pineapple.sh)。
// .gitignore 隐藏 .pineapple/,不进版本控制。clone master HEAD,不 pin commit
// (跟踪上游变化,数字会随 pineapple 改动而漂——这是有意的「下游真实形态」。
//  pineapple 落地新 adapter 优化 wangshu 数字就会跟上,反之亦然)。
replace github.com/Liam0205/pineapple/pine-go => ./.pineapple/pine-go

require github.com/Liam0205/pineapple/pine-go v0.0.0-00010101000000-000000000000

require (
	github.com/Liam0205/wangshu v0.2.0-rc5 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/redis/go-redis/v9 v9.18.0 // indirect
	github.com/tetratelabs/wazero v1.12.0 // indirect
	github.com/yuin/gopher-lua v1.1.2 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	golang.org/x/sys v0.44.0 // indirect
)
