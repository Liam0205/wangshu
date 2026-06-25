//go:build wangshu_p4 && !amd64 && !arm64

// arch_other.go —— P4 PJ8 arch 路由非 amd64/arm64 build stub。
//
// 当前 P4 仅 amd64/arm64 双后端(承 06-backends.md §1)。其它 GOARCH(386/
// mips/riscv64 等)build 下提供编译期可见的 stub:archEmitLoadKReturn 返空
// → archMmapCode 返错(空段被 emitter_nonamd64.go 那条路径同款拒)→
// Compile 返 ErrCompileUnsupportedShape ⇒ TierStuck,行为等价 P1。
package jit

import "errors"

// archCodePage stub(空 struct 占位,跨 arch 编译期可见)。
type archCodePage struct{}

// Addr 占位返 0(永不真用)。
func (*archCodePage) Addr() uintptr { return 0 }

// Length 占位返 0。
func (*archCodePage) Length() int { return 0 }

// Munmap 占位 no-op。
func (*archCodePage) Munmap() error { return nil }

// archEmitLoadKReturn stub:返空 buf(MmapCode 见空段返错)。
func archEmitLoadKReturn(buf []byte, value uint64) []byte {
	_ = value
	return buf
}

// archMmapCode stub:返错(无 amd64/arm64 后端可用)。
func archMmapCode(code []byte) (*archCodePage, error) {
	_ = code
	return nil, errors.New("internal/gibbous/jit: P4 unsupported on this GOARCH (only amd64/arm64)")
}

// archCallJITFull stub:不应被调到(MmapCode 已返错让 Compile 拒)。防御
// 性 panic 让违约场景显式。
func archCallJITFull(codeAddr uintptr, jitCtxAddr uintptr) uint64 {
	_ = codeAddr
	_ = jitCtxAddr
	panic("internal/gibbous/jit: archCallJITFull called on unsupported GOARCH")
}

// archCallJITSpec stub:同 archCallJITFull。
func archCallJITSpec(codeAddr uintptr, jitCtxAddr uintptr, vsBase uintptr) uint64 {
	_ = codeAddr
	_ = jitCtxAddr
	_ = vsBase
	panic("internal/gibbous/jit: archCallJITSpec called on unsupported GOARCH")
}

// archEmitArithSpecAddWithGuard 其它 arch 不支持。
func archEmitArithSpecAddWithGuard(buf []byte, a, b, c uint8, deoptCode uint64) []byte {
	_ = a
	_ = b
	_ = c
	_ = deoptCode
	return buf
}

// archSupportsSpec 其它 arch 不支持。
func archSupportsSpec() bool { return false }
