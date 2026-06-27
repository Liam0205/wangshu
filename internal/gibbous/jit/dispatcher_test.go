//go:build wangshu_p4

package jit

import (
	"reflect"
	"testing"
)

// dispatcher_test.go —— Spike 1 trampoline exit-resume 协议 commit-3a
// dispatcher 函数地址静态测试(承 §9.20.9 (5) Go 端 dispatcher 协议
// + helpers_test.go 同款范本)。
//
// 真接入 prerequisite:trampoline asm 经 `CALL ·dispatchInlineHelper(SB)` 调本
// 函数,asm 链接器经函数符号取地址。本测试经 reflect.ValueOf 验函数符号
// 可被 Go 端取到,确保 trampoline asm 链接不出错。
//
// **archSupportsFrameInline=false 屏蔽真触发**,本测试仅验函数符号存在 +
// emit 地址可取;production 路径不触达本 dispatcher(commit-5 翻闸门后启用)。

func TestDispatchInlineHelper_AddressNonZero(t *testing.T) {
	addr := reflect.ValueOf(dispatchInlineHelper).Pointer()
	if addr == 0 {
		t.Fatal("dispatchInlineHelper 函数地址 = 0,trampoline asm CALL 不能符号化")
	}
	if addr < 0x10000 {
		t.Errorf("dispatchInlineHelper 函数地址 = 0x%X 在 0 页或低于 0x10000,异常",
			addr)
	}
}

func TestDispatchInlineHelper_AddressStable(t *testing.T) {
	addr1 := reflect.ValueOf(dispatchInlineHelper).Pointer()
	addr2 := reflect.ValueOf(dispatchInlineHelper).Pointer()
	if addr1 != addr2 {
		t.Errorf("dispatchInlineHelper 函数地址不一致:0x%X vs 0x%X", addr1, addr2)
	}
}
