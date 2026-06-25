//go:build wangshu_p4 && !amd64

package amd64

// 非 amd64 平台占位 stub——让 wangshu_p4 build 在 arm64 等其它架构主机仍
// 可编译。本平台启用 wangshu_p4 时实际不会经 jit.Compile 触达 amd64 emitter
// (compiler.go 应按 arch dispatch,但当前 PJ8 简化形态共用——非 amd64 时
// Compile 调本 stub,EmitMovRaxImm64/EmitRet 返空 byte slice ⇒ MmapCode 收到
// 空段返错 ⇒ Compile 返错 ⇒ Proto 标 TierStuck 走 crescent)。

// EmitMovRaxImm64 在非 amd64 平台返空(Compile 链下游会因空段返错 fallback)。
func EmitMovRaxImm64(buf []byte, imm uint64) []byte {
	_ = imm
	return buf
}

// EmitRet 在非 amd64 平台返空。
func EmitRet(buf []byte) []byte {
	return buf
}

// EmitMovImm64ToReg 占位。
func EmitMovImm64ToReg(buf []byte, regNum uint8, imm uint64) []byte {
	_ = regNum
	_ = imm
	return buf
}

// EmitNop 占位。
func EmitNop(buf []byte) []byte { return buf }

// EmitCmpRaxImm32 占位。
func EmitCmpRaxImm32(buf []byte, imm int32) []byte {
	_ = imm
	return buf
}

// EmitJmpRel32 占位。
func EmitJmpRel32(buf []byte, rel32 int32) []byte {
	_ = rel32
	return buf
}

// EmitJaeRel32 占位。
func EmitJaeRel32(buf []byte, rel32 int32) []byte {
	_ = rel32
	return buf
}

// EmitCallRel32 占位。
func EmitCallRel32(buf []byte, rel32 int32) []byte {
	_ = rel32
	return buf
}

// EmitCallReg 占位。
func EmitCallReg(buf []byte, regNum uint8) []byte {
	_ = regNum
	return buf
}

// EmitPushReg 占位。
func EmitPushReg(buf []byte, regNum uint8) []byte {
	_ = regNum
	return buf
}

// EmitPopReg 占位。
func EmitPopReg(buf []byte, regNum uint8) []byte {
	_ = regNum
	return buf
}

// EmitLoadKReturnTemplate 占位。
func EmitLoadKReturnTemplate(buf []byte, konst uint64) []byte {
	_ = konst
	return buf
}

// EmitProlog 占位。
func EmitProlog(buf []byte) []byte { return buf }

// EmitEpilog 占位。
func EmitEpilog(buf []byte) []byte { return buf }

// 编码长度常量(让其它包能拿到这些常量,但占位 stub 实际不发射任何字节)。
const (
	EncodedMovRaxImm64Len         = 10
	EncodedRetLen                 = 1
	EncodedMovImm64ToRegLen       = 10
	EncodedNopLen                 = 1
	EncodedCmpRaxImm32Len         = 6
	EncodedJmpRel32Len            = 5
	EncodedJaeRel32Len            = 6
	EncodedCallRel32Len           = 5
	EncodedCallRegLen             = 2
	EncodedPushRegLen             = 1
	EncodedPopRegLen              = 1
	EncodedLoadKReturnTemplateLen = EncodedMovRaxImm64Len + EncodedRetLen
	EncodedPrologLen              = 2
	EncodedEpilogLen              = 2
)
