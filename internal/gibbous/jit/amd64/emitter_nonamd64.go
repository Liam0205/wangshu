//go:build wangshu_p4 && !amd64

package amd64

// Placeholder stubs for non-amd64 platforms — keep the wangshu_p4 build
// compilable on hosts of other architectures such as arm64. When wangshu_p4
// is enabled on such a platform, jit.Compile does not actually reach the amd64
// emitter (compiler.go is supposed to dispatch by arch, but the current PJ8
// simplified form shares this path — on non-amd64, Compile calls these stubs,
// EmitMovRaxImm64/EmitRet return an empty byte slice ⇒ MmapCode gets an empty
// segment and errors ⇒ Compile errors ⇒ Proto is marked TierStuck and falls
// back to crescent).

// EmitMovRaxImm64 returns empty on non-amd64 platforms (downstream in the
// Compile chain the empty segment triggers an error and fallback).
func EmitMovRaxImm64(buf []byte, imm uint64) []byte {
	_ = imm
	return buf
}

// EmitRet returns empty on non-amd64 platforms.
func EmitRet(buf []byte) []byte {
	return buf
}

// EmitMovImm64ToReg placeholder.
func EmitMovImm64ToReg(buf []byte, regNum uint8, imm uint64) []byte {
	_ = regNum
	_ = imm
	return buf
}

// EmitNop placeholder.
func EmitNop(buf []byte) []byte { return buf }

// EmitCmpRaxImm32 placeholder.
func EmitCmpRaxImm32(buf []byte, imm int32) []byte {
	_ = imm
	return buf
}

// EmitJmpRel32 placeholder.
func EmitJmpRel32(buf []byte, rel32 int32) []byte {
	_ = rel32
	return buf
}

// EmitJaeRel32 placeholder.
func EmitJaeRel32(buf []byte, rel32 int32) []byte {
	_ = rel32
	return buf
}

// EmitCallRel32 placeholder.
func EmitCallRel32(buf []byte, rel32 int32) []byte {
	_ = rel32
	return buf
}

// EmitCallReg placeholder.
func EmitCallReg(buf []byte, regNum uint8) []byte {
	_ = regNum
	return buf
}

// EmitPushReg placeholder.
func EmitPushReg(buf []byte, regNum uint8) []byte {
	_ = regNum
	return buf
}

// EmitPopReg placeholder.
func EmitPopReg(buf []byte, regNum uint8) []byte {
	_ = regNum
	return buf
}

// EmitLoadKReturnTemplate placeholder.
func EmitLoadKReturnTemplate(buf []byte, konst uint64) []byte {
	_ = konst
	return buf
}

// EmitProlog placeholder.
func EmitProlog(buf []byte) []byte { return buf }

// EmitEpilog placeholder.
func EmitEpilog(buf []byte) []byte { return buf }

// Encoded length constants (so other packages can reference these, even though
// the placeholder stubs do not actually emit any bytes).
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
