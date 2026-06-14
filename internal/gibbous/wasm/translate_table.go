//go:build wangshu_p3

package wasm

// 表 IC opcode 翻译(PW5,02-translation §3.4 P3 翻译复杂度峰值)。
//
// 核心机制:**IC 快照编译期固化**——编译期读 Proto.IC[pc] 取
// (Kind, Shape=gen, Index, TableRef),烧成 Wasm 立即数;运行期「同表同代次
// (+ 键匹配)」直达 array/node 槽,**跳过哈希**;失效(rehash → gen bump /
// 换表 / 槽 nil)降级走助手(完整查找 + 元方法,byte-equal,06 §1/§2)。
//
// 铁律(06 §1):快路径检查 = 语义分发非投机 guard——helper 永远在 block 末尾
// 兜底,任一层校验失败落到 helper 得正确结果,零 deopt。
//
// 表对象 inline 寻址(object/table.go 布局,arena=linear memory,GCRef=字节偏移):
//   taddr = GCRefOf(tbl) = value 低 48 位;gen = word5(offset40)高 32 位;
//   nodeRef = word3(offset24);array = word2(offset16);
//   node 槽 24 字节步长:key=+0 val=+8 next=+16;array 槽 = arrayRef + idx*8。

import (
	"sync/atomic"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// icSnapshot 是编译期固化的 IC 快照(从 Proto.IC[pc] race-tolerant 读)。
type icSnapshot struct {
	kind     uint8
	gen      uint32 // Shape
	index    uint32 // Index(array 下标 / node 槽号)
	tableRef uint32 // TableRef(目标表 arena 偏移低 32 位)
}

// snapshotICSlot race-tolerant 读 Proto.IC[pc](06 §4.3.1)。多 State 并发下
// P1 仍在写 IC,读到半新半旧不爆炸——运行期校验失败走助手兜底。
func snapshotICSlot(proto *bytecode.Proto, pc int32) icSnapshot {
	if int(pc) >= len(proto.IC) {
		return icSnapshot{}
	}
	slot := &proto.IC[pc]
	return icSnapshot{
		kind:     slot.Kind, // uint8 对齐字节读原子
		gen:      atomic.LoadUint32(&slot.Shape),
		index:    atomic.LoadUint32(&slot.Index),
		tableRef: atomic.LoadUint32(&slot.TableRef),
	}
}

// emitGenCheck 发 gen 校验条件:`(word5 >> 32) as i32 == SNAP_GEN`。
// taddrConst 是表字节地址 i32 立即数。结果 i32 留栈顶(供 ifVoid)。
func (c *Compiler) emitGenCheck(em *emitter, taddrConst int32, snapGen uint32) {
	em.i32Const(taddrConst)
	em.i64Load(tblGenOff) // word5
	em.i64Const(32)
	em.i64ShrU()
	em.i32WrapI64()
	em.i32Const(int32(snapGen))
	em.i32Eq()
}

// emitHelperEpilogue 发表 IC 慢路径助手调用 + 错误冒泡:
//
//	(local.set $st (call helper(base, pc, a, b, c)))
//	(if (i32.eq $st 1) (then (return 1)))
func (c *Compiler) emitHelperEpilogue5(em *emitter, helper uint32, pc, a, b, cc int32) {
	em.localGet(localBase)
	em.i32Const(pc)
	em.i32Const(a)
	em.i32Const(b)
	em.i32Const(cc)
	em.call(helper)
	em.localTee(localI32)
	em.i32Const(1)
	em.i32Eq()
	em.ifVoid()
	em.i32Const(1)
	em.ret()
	em.end()
}

// emitHelperEpilogue4 同上但 4 参(getglobal/setglobal:base,pc,a,bx)。
func (c *Compiler) emitHelperEpilogue4(em *emitter, helper uint32, pc, a, bx int32) {
	em.localGet(localBase)
	em.i32Const(pc)
	em.i32Const(a)
	em.i32Const(bx)
	em.call(helper)
	em.localTee(localI32)
	em.i32Const(1)
	em.i32Eq()
	em.ifVoid()
	em.i32Const(1)
	em.ret()
	em.end()
}

// emitGetGlobal GETGLOBAL A Bx —— R(A) := Gtable[K(Bx)](02 §3.4.4)。
//
// globals 表恒定(编译期烧地址立即数);key 是常量 K(Bx) 同一 pc 恒定 → 省键校验;
// globals 恒 NodeHit(asize=0)。inline:gen 校验 → node 槽取值 → 非 nil 则 store。
//
//	block $done
//	  if (gen == SNAP_GEN)
//	    $vb := i64.load[nodeRef + SNAP_INDEX*24+8]
//	    if ($vb != nil) { R(A) := $vb; br 2 }
//	  end
//	  <helper h_getglobal>(gen miss / 槽 nil)
//	end
func (c *Compiler) emitGetGlobal(em *emitter, proto *bytecode.Proto, ins bytecode.Instruction, pc int32) {
	a := int32(bytecode.A(ins))
	bx := int32(bytecode.Bx(ins))
	snap := snapshotICSlot(proto, pc)
	taddr := int32(c.host.GlobalsRaw() & payloadMaskU64) // globals 字节地址(GCRef)

	// 只在 NodeHit 快照可信时 inline;否则纯助手(等价无 IC,06 §3)。
	if snap.kind != bytecode.ICKindNodeHit {
		c.emitHelperEpilogue4(em, helperGetGlobal, pc, a, bx)
		return
	}

	valOff := snap.index*nodeStrideBytes + nodeValOff

	em.block() // $done
	c.emitGenCheck(em, taddr, snap.gen)
	em.ifVoid()
	// node 基址 = wrap(i64.load[taddr+24])
	em.i32Const(taddr)
	em.i64Load(tblNodeOff)
	em.i32WrapI64()
	em.localSet(localI32b)
	// $vb = i64.load[nodeRef + valOff]
	em.localGet(localI32b)
	em.i64Load(valOff)
	em.localTee(localI64c)
	em.i64Const(nilRawU64())
	em.i64Ne()
	em.ifVoid()
	// R(A) := $vb;br 2(跳出 $done,跳过 helper)
	em.localGet(localBase)
	em.localGet(localI64c)
	em.i64Store(8 * uint32(a))
	em.br(2)
	em.end() // if vb!=nil
	em.end() // if gen
	// 慢路径
	c.emitHelperEpilogue4(em, helperGetGlobal, pc, a, bx)
	em.end() // block $done
}

// emitSetGlobal SETGLOBAL A Bx —— Gtable[K(Bx)] := R(A)(02 §3.4.4)。
//
// 改已存在键的快路径(改值不 bump gen,IC 持续有效)。inline:gen 校验 → 当前
// 槽 val 非 nil(键存在)→ 写新值。删除 / 新增键 / gen miss → 助手。
//
//	block $done
//	  if (gen == SNAP_GEN)
//	    $i32b := node base; if (i64.load[base+valOff] != nil)
//	      { i64.store[base+valOff] := R(A); br 2 }
//	  end
//	  <helper h_setglobal>
//	end
func (c *Compiler) emitSetGlobal(em *emitter, proto *bytecode.Proto, ins bytecode.Instruction, pc int32) {
	a := int32(bytecode.A(ins))
	bx := int32(bytecode.Bx(ins))
	snap := snapshotICSlot(proto, pc)
	taddr := int32(c.host.GlobalsRaw() & payloadMaskU64)

	if snap.kind != bytecode.ICKindNodeHit {
		c.emitHelperEpilogue4(em, helperSetGlobal, pc, a, bx)
		return
	}

	valOff := snap.index*nodeStrideBytes + nodeValOff

	em.block() // $done
	c.emitGenCheck(em, taddr, snap.gen)
	em.ifVoid()
	// node 基址
	em.i32Const(taddr)
	em.i64Load(tblNodeOff)
	em.i32WrapI64()
	em.localSet(localI32b)
	// 当前槽 val != nil(键已存在)
	em.localGet(localI32b)
	em.i64Load(valOff)
	em.i64Const(nilRawU64())
	em.i64Ne()
	em.ifVoid()
	// i64.store[base+valOff] := R(A);br 2
	em.localGet(localI32b)
	em.localGet(localBase)
	em.i64Load(8 * uint32(a))
	em.i64Store(valOff)
	em.br(2)
	em.end() // if val!=nil
	em.end() // if gen
	c.emitHelperEpilogue4(em, helperSetGlobal, pc, a, bx)
	em.end() // block $done
}
