// Package difftest hosts the cross-implementation difftest harness:
// Wangshu vs official Lua 5.1.5 vs gopher-lua, byte-equal output (12 §3).
//
// Currently a placeholder: M14 wires in the harness and the generator; until
// then `go test ./test/difftest/...` PASSes directly (empty-package semantics,
// engineering.md §3.1).
package difftest
