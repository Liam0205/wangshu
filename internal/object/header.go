// Package object provides field-level read/write helpers for the six arena-resident
// GC object types defined in docs/design/p1-interpreter/01-value-object-model.md §4-§5.
//
// 本阶段(M3):提供布局 helper 与「手动分配」的构造函数。GCHeader 颜色/sweep 链由 gc 包
// (M5)写入,这里只定义位布局与字段访问 API,allocateRaw 调用方负责字数与 otype 的设置。
//
// 重要不变式(均承上游设计文档):
//   - 字数公式与 06 §1.3 一致;
//   - GCRef 偏移寻址,grow 后仍有效(arena 红利);
//   - 「头对象」入 sweep 链(M5),array/node/valueStack/callInfo 是附属块不入链(06 §1.3);
//   - Table 的 gen 代次字段、Upvalue 开放态 nextOpen、Proto LocVars 等回填请求均按 01 兑现。
package object

import (
	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/value"
)

// OBJType 是 GCHeader 中标识「头对象」实际类型的枚举(01 §4)。
//
// 与 value tag 不同:value tag 只区分 8 个非数字类型;OBJType 区分**带 GCHeader 的头对象**,
// 含 Upvalue(无对应 value tag,因为 upvalue 不直接出现在脚本可见的 Value 里)。
type OBJType uint8

const (
	OBJ_NONE     OBJType = 0
	OBJ_STRING   OBJType = 1
	OBJ_TABLE    OBJType = 2
	OBJ_CLOSURE  OBJType = 3
	OBJ_USERDATA OBJType = 4
	OBJ_THREAD   OBJType = 5
	OBJ_PROTO    OBJType = 6 // P1 不用(Proto 住 Go 堆),保留枚举供未来 in-arena Proto
	OBJ_UPVAL    OBJType = 7
)

// GCHeader 位布局(01 §4):
//
//	bits [7:0]    otype     OBJType
//	bits [9:8]    color     0=white0 / 1=white1 / 2=gray / 3=black
//	bits [10]     fixed     1=永不回收
//	bits [11]     hasGCNext 链表标志
//	bits [15:12]  flags     类型私有标志(如 table 有无 metatable 的快判位)
//	bits [63:16]  gcnext    48-bit sweep 链下一对象偏移(0=链尾)
const (
	hdrOTypeShift   = 0
	hdrOTypeMask    = uint64(0xFF)
	hdrColorShift   = 8
	hdrColorMask    = uint64(0x3) << hdrColorShift
	hdrFixedBit     = uint64(1) << 10
	hdrHasGCNextBit = uint64(1) << 11
	hdrFlagsShift   = 12
	hdrFlagsMask    = uint64(0xF) << hdrFlagsShift
	hdrGCNextShift  = 16
)

// HeaderOf reads the GCHeader word of a head object.
func HeaderOf(a *arena.Arena, ref arena.GCRef) uint64 { return a.WordAt(ref) }

// SetHeader writes the GCHeader word.
func SetHeader(a *arena.Arena, ref arena.GCRef, h uint64) { a.SetWordAt(ref, h) }

// MakeHeader composes a GCHeader (caller-controlled color/flags/gcnext usually start at 0).
func MakeHeader(otype OBJType, color uint8, fixed bool, flags uint8, gcnext arena.GCRef) uint64 {
	h := uint64(otype) & hdrOTypeMask
	h |= (uint64(color) & 0x3) << hdrColorShift
	if fixed {
		h |= hdrFixedBit
	}
	h |= (uint64(flags) & 0xF) << hdrFlagsShift
	if gcnext != 0 {
		h |= hdrHasGCNextBit | (uint64(gcnext) << hdrGCNextShift)
	}
	return h
}

// Field accessors on a header word.
func OTypeOf(h uint64) OBJType      { return OBJType(h & hdrOTypeMask) }
func ColorOf(h uint64) uint8        { return uint8((h & hdrColorMask) >> hdrColorShift) }
func IsFixed(h uint64) bool         { return h&hdrFixedBit != 0 }
func HasGCNext(h uint64) bool       { return h&hdrHasGCNextBit != 0 }
func FlagsOf(h uint64) uint8        { return uint8((h & hdrFlagsMask) >> hdrFlagsShift) }
func GCNextOf(h uint64) arena.GCRef { return arena.GCRef(h >> hdrGCNextShift) }

// SetColor returns h with the color field replaced.
func SetColor(h uint64, c uint8) uint64 {
	return (h &^ hdrColorMask) | ((uint64(c) & 0x3) << hdrColorShift)
}

// SetFlags returns h with the flags field replaced.
func SetFlags(h uint64, f uint8) uint64 {
	return (h &^ hdrFlagsMask) | ((uint64(f) & 0xF) << hdrFlagsShift)
}

// SetGCNext returns h with the gcnext field replaced (and HasGCNext bit toggled).
func SetGCNext(h uint64, ref arena.GCRef) uint64 {
	h &^= (uint64(0xFFFFFFFFFFFF) << hdrGCNextShift) | hdrHasGCNextBit
	if ref != 0 {
		h |= hdrHasGCNextBit | (uint64(ref) << hdrGCNextShift)
	}
	return h
}

// Color constants (01 §4.2 / 06 §4.2).
const (
	ColorWhite0 = 0
	ColorWhite1 = 1
	ColorGray   = 2
	ColorBlack  = 3
)

// allocateRaw 分配一个头对象,写入初始 GCHeader,返回 GCRef。
//
// M3 阶段:暴露给同包构造函数使用;color 默认为 white0,gcnext=0(M5 GC 接入后由 collector
// 改写并挂入 sweep 链)。flags 由调用方按对象类型语义自定义。
func allocateRaw(a *arena.Arena, otype OBJType, words uint32, flags uint8) arena.GCRef {
	ref := a.AllocWords(words)
	SetHeader(a, ref, MakeHeader(otype, ColorWhite0, false, flags, 0))
	return ref
}

// 字段读写工具:把对象内的某 word 偏移转化为 arena GCRef 并读写。
func wordAt(a *arena.Arena, ref arena.GCRef, idx uint32) uint64 {
	return a.WordAt(ref + arena.GCRef(idx*8))
}

func setWordAt(a *arena.Arena, ref arena.GCRef, idx uint32, v uint64) {
	a.SetWordAt(ref+arena.GCRef(idx*8), v)
}

// valueAt / setValueAt:用于读写 NaN-boxed Value 字段。
func valueAt(a *arena.Arena, ref arena.GCRef, idx uint32) value.Value {
	return value.Value(wordAt(a, ref, idx))
}

func setValueAt(a *arena.Arena, ref arena.GCRef, idx uint32, v value.Value) {
	setWordAt(a, ref, idx, uint64(v))
}
