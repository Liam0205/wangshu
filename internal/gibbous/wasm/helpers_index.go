//go:build wangshu_p3

package wasm

import "github.com/Liam0205/wangshu/internal/value"

// imported helper 的 Wasm function index(02-translation §6.4)。
//
// gibbous module import 一组 Go 助手(env.h_*),import 的 func 占据 Wasm
// function index 空间的前若干位(import 先于本地 func)。这些常量是各助手
// 在 import 列表里的索引,与 module 组装(module.go)的 import 声明顺序
// **严格一致**。
//
// PW2 用到的助手(直线 opcode 的 GETUPVAL/SETUPVAL/RETURN):
const (
	helperGetUpval  uint32 = iota // env.h_getupval (base i32, b i32) -> (i64)
	helperSetUpval                // env.h_setupval (base i32, b i32, val i64) -> ()
	helperReturn                  // env.h_return   (base i32, pc i32, a i32, b i32) -> (i32)
	helperSafepoint               // env.h_safepoint(base i32, pc i32) -> ()  (PW4 回边用,先声明)
	numHelpers
)

// NaN-box raw u64 工具:编译期烧立即数用(translate.go)。
func nilRawU64() uint64   { return uint64(value.Nil) }
func trueRawU64() uint64  { return uint64(value.True) }
func falseRawU64() uint64 { return uint64(value.False) }
