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
	helperArith                   // host.h_arith   (base,pc,op,b,c,a i32) -> (i32 status)  PW3
	helperUnm                     // host.h_unm     (base,pc,b,a i32) -> (i32 status)        PW3
	helperLen                     // host.h_len     (base,pc,b,a i32) -> (i32 status)        PW3
	helperConcat                  // host.h_concat  (base,pc,a,b,c i32) -> (i32 status)      PW3
	helperCompare                 // host.h_compare (base,pc,op,b,c i32) -> (i32 packed)     PW4
	helperEq                      // host.h_eq      (base,pc,b,c i32) -> (i32 packed)        PW4
	helperForPrep                 // host.h_forprep (base,pc,a i32) -> (i32 status)          PW4
	helperGetTable                // host.h_gettable (base,pc,a,b,c i32) -> (i32 status)     PW5
	helperSetTable                // host.h_settable (base,pc,a,b,c i32) -> (i32 status)     PW5
	helperGetGlobal               // host.h_getglobal(base,pc,a,bx i32) -> (i32 status)      PW5
	helperSetGlobal               // host.h_setglobal(base,pc,a,bx i32) -> (i32 status)      PW5
	helperSelf                    // host.h_self     (base,pc,a,b,c i32) -> (i32 status)     PW5
	helperNewTable                // host.h_newtable (base,pc,a,b,c i32) -> (i32 status)     PW5
	helperSetList                 // host.h_setlist  (base,pc,a,b,c i32) -> (i32 status)     PW5
	helperCall                    // host.h_call     (base,pc,a,b,c i32) -> (i64 newbase/-1) PW6
	helperTailCall                // host.h_tailcall (base,pc,a,b,c i32) -> (i32 0/1/2)       PW6
	helperClosure                 // host.h_closure  (base,pc,a,bx i32) -> (i32 status)       PW7
	helperClose                   // host.h_close    (base,pc,a i32) -> (i32 status)          PW7
	helperTForLoop                // host.h_tforloop (base,pc,a,c i32) -> (i64 newbase/-1/-2)  PW4b
	helperCallErr                 // host.h_callerr  () -> ()  PW10 R3:call_indirect 直调失败补弹遗留 gibbous 帧
	numHelpers
)

// NaN-box raw u64 工具:编译期烧立即数用(translate.go)。
func nilRawU64() uint64   { return uint64(value.Nil) }
func trueRawU64() uint64  { return uint64(value.True) }
func falseRawU64() uint64 { return uint64(value.False) }

// tagShift / tag 常量:NOT/LEN 的 tag 提取(value.Tag = v>>48)。
const (
	tagShiftBits = 48
	tagStringU64 = uint64(value.TagString) // 0xFFFB
	tagTableU64  = uint64(value.TagTable)  // 0xFFFC
)

// qNanBoxBase 是 IsNumber 判定边界(value.IsNumber: v < 0xFFF8...)。
const qNanBoxBase uint64 = 0xFFF8_0000_0000_0000

// canonNaNU64 是规范 NaN(value 包 canonicalize 目标,01 §3.4)。
const canonNaNU64 uint64 = 0x7FF8_0000_0000_0000

// PW5 表 IC inline 用常量。
const (
	// payloadMaskU64 = GCRefOf 的低 48 位掩码(value.go payloadMask)——
	// NaN-box value 的低 48 位即对象 arena 字节偏移(GCRef)。
	payloadMaskU64 uint64 = 0x0000_FFFF_FFFF_FFFF

	// 表对象字段字节偏移(object/table.go 布局:word_n offset=8*n)。
	tblArrayOff = 16 // word2: arrayRef
	tblNodeOff  = 24 // word3: nodeRef
	tblGenOff   = 40 // word5: lastfree | gen(gen 在高 32 位)
	// node 槽步长(3 字 = 24 字节);key=+0 val=+8 next=+16。
	nodeStrideBytes = 24
	nodeValOff      = 8
)

// CI 段帧布局常量(PW10 零跨界 ③b:Wasm 内 RETURN 拆帧动态读段帧)。与 crescent
// state.go 的 ciWords / writeCISeg / packCIWord2 布局**严格一致**:每帧 4 word=32
// 字节;word0[31:0]=base|[63:32]=funcIdx;word1[31:0]=top|[63:32]=pc;word2 低 32
// =protoID、[47:32]=nresults(int16)、bit50=gibbous;word3=cl。帧字节地址 =
// load(ciSegBaseAddr) + depth*ciFrameBytes + word*8。
const (
	ciFrameBytes  = 32 // ciWords(4) * 8
	ciWord1Off    = 8  // word1: top | pc
	ciWord2Off    = 16 // word2: protoID | nresults | flags
	ciGibbousBit  = uint64(1) << 50
	maxReturnFast = 8 // ③b 快路径展开 moveResults 的 nret 上界(超出走 helperReturn)
)
