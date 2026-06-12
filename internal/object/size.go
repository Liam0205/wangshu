// SizeOf — 六类头对象的字节尺寸单一事实源。
//
// gc 包的 Intern 计费 / objectBytes 统计 / freeObject 释放此前各持一份手写
// 公式(共四处),freeObject 的尺寸错一个字 = freelist 错桶 = 复用时相邻
// 对象内存重叠(UAF 级,生产模式无检测)。布局变更只改本文件。
package object

import "github.com/Liam0205/wangshu/internal/arena"

// StringObjectBytes 返回 String 对象的总字节(头 2 字 + 内容 + NUL,8 对齐)。
func StringObjectBytes(byteLen uint32) uint32 {
	return stringWords(byteLen) * 8
}

// TableHeadBytes 返回 Table 头对象字节(6 字;array/node 附属块另计)。
func TableHeadBytes() uint32 { return tableHeadWords * 8 }

// TableArrayBytes / TableNodeBytes 返回附属块字节。
func TableArrayBytes(asize uint32) uint32 { return asize * 8 }
func TableNodeBytes(hsize uint32) uint32  { return hsize * nodeWords * 8 }

// ClosureBytes 返回 closure 对象字节(头 2 字 + nupvals 槽)。
func ClosureBytes(nupvals uint16) uint32 { return (2 + uint32(nupvals)) * 8 }

// UserdataBytes 返回 userdata 对象字节(头 4 字 + payload,8 对齐)。
func UserdataBytes(payloadLen uint32) uint32 {
	return userdataWords(payloadLen) * 8
}

// ThreadHeadBytes / ThreadStackBytes / ThreadCIBytes 返回 Thread 头与附属块字节。
func ThreadHeadBytes() uint32                 { return threadHeadWords * 8 }
func ThreadStackBytes(stackCap uint32) uint32 { return stackCap * 8 }
func ThreadCIBytes(ciCap uint32) uint32       { return ciCap * 4 * 8 }

// UpvalueBytes 返回 Upvalue 对象字节(3 字)。
func UpvalueBytes() uint32 { return 3 * 8 }

// SizeOf 返回头对象自身的字节数(不含 Table/Thread 的附属块——它们由
// 调用方按需另查,因为统计口径(pacing 估算)与释放口径(逐块归还)不同)。
func SizeOf(a *arena.Arena, ref arena.GCRef, ot OBJType) uint32 {
	switch ot {
	case OBJ_STRING:
		return StringObjectBytes(StringLen(a, ref))
	case OBJ_TABLE:
		return TableHeadBytes()
	case OBJ_CLOSURE:
		return ClosureBytes(ClosureNUpvals(a, ref))
	case OBJ_USERDATA:
		return UserdataBytes(UserdataLen(a, ref))
	case OBJ_THREAD:
		return ThreadHeadBytes()
	case OBJ_UPVAL:
		return UpvalueBytes()
	}
	return 0
}
