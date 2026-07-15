module github.com/Liam0205/wangshu/benchmarks/pineapple

go 1.26.2

// wangshu: this repo, two levels up
replace github.com/Liam0205/wangshu => ../../

// pineapple: pulled into .pineapple/ via `make fetch` (see
// scripts/fetch-pineapple.sh). .gitignore hides .pineapple/ from version
// control. Clones master HEAD, no pinned commit (tracks upstream; numbers
// drift with pineapple changes -- intentionally the "real downstream
// shape": when pineapple ships a new adapter optimization the wangshu
// numbers follow, and vice versa).
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
