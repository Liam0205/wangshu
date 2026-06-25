//go:build wangshu_p4 && amd64

// arch_amd64.go —— P4 PJ8 arch 路由 amd64 实装(对位 arch_arm64.go)。
//
// 把 compiler.go / code.go 里硬编码的 jitamd64 依赖移到此 arch 适配层,
// 让 jit 包主体不依赖具体 GOARCH,arm64 build 下自动切到 jitarm64。
//
// 承 docs/design/p4-method-jit/06-backends.md §1「共享骨架 + per-arch 发射器」
// 决议——per-arch 发射函数按 build tag 各一份,jit 主包 import 中性接口。
package jit

import (
	jitamd64 "github.com/Liam0205/wangshu/internal/gibbous/jit/amd64"
)

// archCodePage 是 arch 抽象的可执行段——本 build 下别名 jitamd64.CodePage。
type archCodePage = jitamd64.CodePage

// archEmitLoadKReturn 发射「mov RAX, value; ret」直线模板(amd64 端 11 字节)。
// 常量族(LOADK/LOADBOOL/LOADNIL)烧 NaN-box value;prelude/比较折叠族 RAX
// 是 dummy(由 Run 端忽略),value 仍要写入(模板字节数固定)。
func archEmitLoadKReturn(buf []byte, value uint64) []byte {
	buf = jitamd64.EmitMovRaxImm64(buf, value)
	buf = jitamd64.EmitRet(buf)
	return buf
}

// archMmapCode 把 code 写入 W^X 段(PROT_RW alloc → copy → PROT_RX 翻面)。
func archMmapCode(code []byte) (*archCodePage, error) {
	return jitamd64.MmapCode(code)
}

// archCallJITFull 跳进 mmap 段(完整 trampoline:保存 callee-saved + 装
// jitContext 到 r15 + CALL + 恢复)。返 RAX。
func archCallJITFull(codeAddr uintptr, jitCtxAddr uintptr) uint64 {
	return jitamd64.CallJITFull(codeAddr, jitCtxAddr)
}
