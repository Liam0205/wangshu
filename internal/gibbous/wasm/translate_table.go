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

// --- GETTABLE / SETTABLE(PW5-b,动态键匹配)---
//
// 控制结构(br 深度恒定,避免深嵌套计数):
//
//	block $done        ; depth 1(成功:跳过助手)
//	  block $slow      ; depth 0(放弃 inline:落到助手)
//	    <表 guard + 键匹配 + 槽非 nil,任一失败 br_if 0 → $slow>
//	    <命中:store / load;br 1 → $done>
//	  end ; $slow
//	  <helper>
//	end ; $done
//
// inline 仅覆盖 byte-equal 可证形态:① 常量键(同表同 gen ⟹ 缓存 Index 仍映射
// 同键,省键匹配,同 GETGLOBAL);② 寄存器键 + ArrayHit(数值 f64 == Index+1)。
// 寄存器键 + NodeHit(normKey/keyEqual inline 脆弱)/ MonoMeta → 纯助手。

// tableInlineable 判某表 IC 点是否走 inline 快路径(否则纯助手)。
// regKey=true 表示键是寄存器(动态),false 表示常量键。
func tableInlineable(snap icSnapshot, regKey bool) bool {
	switch snap.kind {
	case bytecode.ICKindArrayHit:
		return true // 常量键省匹配;寄存器键走数值匹配
	case bytecode.ICKindNodeHit:
		return !regKey // 仅常量键(寄存器键 NodeHit 走助手)
	default:
		return false // None / MonoMeta / Mega
	}
}

// emitTableGuard 发表 guard:IsTable + 同 TableRef + 同 gen(任一失败 br_if 0)。
// 入口栈空;出口:localI64c=表值(已消费),localI32b=表字节地址(taddr,i32)。
// 前置:必须在 block $slow(depth 0)内调用。
func (c *Compiler) emitTableGuard(em *emitter, regB int, snap icSnapshot) {
	// vt := R(B);localI64c = vt;localI32b = taddr(低 48 位 wrap i32)
	em.localGet(localBase)
	em.i64Load(8 * uint32(regB))
	em.localTee(localI64c)
	em.i64Const(payloadMaskU64)
	em.i64And()
	em.i32WrapI64()
	em.localSet(localI32b)
	// IsTable: (vt >> 48) == TagTable
	em.localGet(localI64c)
	em.i64Const(48)
	em.i64ShrU()
	em.i64Const(uint64(tagTableU64))
	em.i64Eq()
	em.i32Eqz()
	em.brIf(0) // → $slow
	// 同 TableRef: taddr(i32) == SNAP_TABLEREF
	em.localGet(localI32b)
	em.i32Const(int32(snap.tableRef))
	em.i32Eq()
	em.i32Eqz()
	em.brIf(0)
	// 同 gen: (i64.load[taddr+40] >> 32) wrap == SNAP_GEN
	em.localGet(localI32b)
	em.i64Load(tblGenOff)
	em.i64Const(32)
	em.i64ShrU()
	em.i32WrapI64()
	em.i32Const(int32(snap.gen))
	em.i32Eq()
	em.i32Eqz()
	em.brIf(0)
}

// emitArrayKeyMatch 发寄存器键 ArrayHit 数值匹配:IsNumber(key) 且 f64(key) ==
// Index+1(arrayIndex 命中 ⟺ 整数键 == Index+1)。失败 br_if 0 → $slow。
func (c *Compiler) emitArrayKeyMatch(em *emitter, regC int, snap icSnapshot) {
	em.localGet(localBase)
	em.i64Load(8 * uint32(regC))
	em.localTee(localI64c)
	// IsNumber: key < qNanBoxBase
	em.i64Const(qNanBoxBase)
	em.i64LtU()
	em.i32Eqz()
	em.brIf(0)
	// f64(key) == Index+1
	em.localGet(localI64c)
	em.f64ReinterpretI64()
	em.f64Const(float64(snap.index) + 1)
	em.f64Eq()
	em.i32Eqz()
	em.brIf(0)
}

// emitSlotAddr 把命中槽的字节地址压栈(i32):
//
//	ArrayHit: wrap(i64.load[taddr+16]) + Index*8
//	NodeHit:  wrap(i64.load[taddr+24]) + (Index*24+8)
//
// 返回该槽相对附属块基址的字节 offset(供 i64.load/store 立即数)与基址在栈顶。
// 实装:压栈基址 i32,offset 由调用方作 i64.load/store 立即数。
func (c *Compiler) emitSlotBase(em *emitter, snap icSnapshot) uint32 {
	if snap.kind == bytecode.ICKindArrayHit {
		em.localGet(localI32b)
		em.i64Load(tblArrayOff)
		em.i32WrapI64()
		return snap.index * 8
	}
	// NodeHit
	em.localGet(localI32b)
	em.i64Load(tblNodeOff)
	em.i32WrapI64()
	return snap.index*nodeStrideBytes + nodeValOff
}

// emitGetTable GETTABLE A B C —— R(A) := R(B)[RK(C)](02 §3.4.2)。
func (c *Compiler) emitGetTable(em *emitter, proto *bytecode.Proto, ins bytecode.Instruction, pc int32) {
	a := int32(bytecode.A(ins))
	b := int32(bytecode.B(ins))
	cc := int32(bytecode.C(ins))
	snap := snapshotICSlot(proto, pc)
	regKey := !bytecode.IsK(int(cc))

	if !tableInlineable(snap, regKey) {
		c.emitHelperEpilogue5(em, helperGetTable, pc, a, b, cc)
		return
	}

	em.block() // $done
	em.block() // $slow
	c.emitTableGuard(em, int(b), snap)
	if regKey && snap.kind == bytecode.ICKindArrayHit {
		c.emitArrayKeyMatch(em, int(cc), snap)
	}
	// 槽值 → localI64c;非 nil 校验
	slotOff := c.emitSlotBase(em, snap)
	em.i64Load(slotOff)
	em.localTee(localI64c)
	em.i64Const(nilRawU64())
	em.i64Ne()
	em.i32Eqz()
	em.brIf(0) // 槽 nil → $slow(可能 __index)
	// 命中:R(A) := localI64c;br $done
	em.localGet(localBase)
	em.localGet(localI64c)
	em.i64Store(8 * uint32(a))
	em.br(1) // → $done
	em.end() // $slow
	c.emitHelperEpilogue5(em, helperGetTable, pc, a, b, cc)
	em.end() // $done
}

// emitSetTable SETTABLE A B C —— R(A)[RK(B)] := RK(C)(02 §3.4.3)。
// 改已存在键的快路径(改值不 bump gen)。删除(val nil)/新增/串常量值 → 助手。
func (c *Compiler) emitSetTable(em *emitter, proto *bytecode.Proto, ins bytecode.Instruction, pc int32) {
	a := int32(bytecode.A(ins))
	b := int32(bytecode.B(ins))
	cc := int32(bytecode.C(ins))
	snap := snapshotICSlot(proto, pc)
	regKey := !bytecode.IsK(int(b))

	// 值 RK(C) 是字符串常量 → 编译期烧不出真 GCRef → 整条降级助手。
	valStrConst := bytecode.IsK(int(cc)) && proto.IsStringConst(bytecode.KIdx(int(cc)))
	if valStrConst || !tableInlineable(snap, regKey) {
		c.emitHelperEpilogue5(em, helperSetTable, pc, a, b, cc)
		return
	}

	em.block() // $done
	em.block() // $slow
	// 值非 nil(置 nil = 删除 → 助手);先取值到 localI64a(避免被 guard 覆盖 localI64c)
	c.loadRK(em, proto, int(cc))
	em.localTee(localI64a)
	em.i64Const(nilRawU64())
	em.i64Ne()
	em.i32Eqz()
	em.brIf(0) // val nil → $slow
	c.emitTableGuard(em, int(a), snap)
	if regKey && snap.kind == bytecode.ICKindArrayHit {
		c.emitArrayKeyMatch(em, int(b), snap)
	}
	// 当前槽非 nil(键已存在,改值快路径);slotBase 压栈,留作后续 store 基址
	slotOff := c.emitSlotBase(em, snap)
	em.localTee(localI32b) // 槽附属块基址 → localI32b(复用:guard 后 taddr 已不需)
	em.i64Load(slotOff)
	em.i64Const(nilRawU64())
	em.i64Ne()
	em.i32Eqz()
	em.brIf(0) // 当前槽 nil → $slow(新增键,可能 rehash)
	// 命中:slot := val(localI64a);br $done
	em.localGet(localI32b)
	em.localGet(localI64a)
	em.i64Store(slotOff)
	em.br(1) // → $done
	em.end() // $slow
	c.emitHelperEpilogue5(em, helperSetTable, pc, a, b, cc)
	em.end() // $done
}

// emitSelf SELF A B C —— R(A+1) := R(B); R(A) := R(B)[RK(C)](02 §3.4.5)。
//
// self 传递 R(A+1):=R(B) 必须先于 IC 查找(execute.go:136),A+1≠B 无冲突。
// IC 查找与 GETTABLE 同构;miss → h_self(助手内含 self 传递,幂等)。
// 方法名键通常是字符串常量 → NodeHit const-key(省键匹配)。
func (c *Compiler) emitSelf(em *emitter, proto *bytecode.Proto, ins bytecode.Instruction, pc int32) {
	a := int32(bytecode.A(ins))
	b := int32(bytecode.B(ins))
	cc := int32(bytecode.C(ins))
	snap := snapshotICSlot(proto, pc)
	regKey := !bytecode.IsK(int(cc))

	// ① self 传递:R(A+1) := R(B)(无条件,先于 IC)。
	em.localGet(localBase)
	em.localGet(localBase)
	em.i64Load(8 * uint32(b))
	em.i64Store(8 * uint32(a+1))

	if !tableInlineable(snap, regKey) {
		c.emitHelperEpilogue5(em, helperSelf, pc, a, b, cc)
		return
	}

	// ② R(A) := R(B)[RK(C)](与 GETTABLE 同构)。
	em.block() // $done
	em.block() // $slow
	c.emitTableGuard(em, int(b), snap)
	if regKey && snap.kind == bytecode.ICKindArrayHit {
		c.emitArrayKeyMatch(em, int(cc), snap)
	}
	slotOff := c.emitSlotBase(em, snap)
	em.i64Load(slotOff)
	em.localTee(localI64c)
	em.i64Const(nilRawU64())
	em.i64Ne()
	em.i32Eqz()
	em.brIf(0) // 槽 nil → $slow
	em.localGet(localBase)
	em.localGet(localI64c)
	em.i64Store(8 * uint32(a))
	em.br(1) // → $done
	em.end() // $slow
	c.emitHelperEpilogue5(em, helperSelf, pc, a, b, cc)
	em.end() // $done
}

// --- NEWTABLE / SETLIST(PW5-d,无 IC,纯助手)---
//
// gibbous 代码自身从不分配(00-overview §8);分配 + GC + 批量写 + 可能 rehash
// 全在 imported 助手内同步完成,助手返回时 GC 已完成,Wasm 侧无感。
// 内联翻译复杂且收益低(表构造非热循环主体)→ 走助手最简(02 §3.4.6/§3.4.7)。

// emitNewTable NEWTABLE A B C —— R(A) := {}(经 h_newtable,分配+setReg+safepoint
// 全助手内)。
func (c *Compiler) emitNewTable(em *emitter, ins bytecode.Instruction, pc int32) {
	a := int32(bytecode.A(ins))
	b := int32(bytecode.B(ins))
	cc := int32(bytecode.C(ins))
	c.emitHelperEpilogue5(em, helperNewTable, pc, a, b, cc)
}

// emitSetList SETLIST A B C —— 表构造批量填数组(经 h_setlist;C=0 取下一指令为
// 大批次号,助手内自取 Proto.Code[pc])。
func (c *Compiler) emitSetList(em *emitter, ins bytecode.Instruction, pc int32) {
	a := int32(bytecode.A(ins))
	b := int32(bytecode.B(ins))
	cc := int32(bytecode.C(ins))
	c.emitHelperEpilogue5(em, helperSetList, pc, a, b, cc)
}

// --- CALL(PW6-a 三向分派 + PW10 R3 call_indirect 直调)---
//
// CALL A B C —— R(A)(R(A+1..A+B-1)),返回回填 R(A..A+C-2)。经 h_call(DoCall)
// 三态分派(04-trampoline §3 + PW10 R3):
//   - < 0(-1):错误 → 自身 return 1(status 链冒泡,§4.1);
//   - 偶数(8 倍数):done —— host/crescent/__call/无-slot gibbous 已同步跑完,
//     值 = 刷新后本帧 base 字节偏移(PW6 base 刷新),local.set $base 续算;
//   - 奇数 (slot<<1)|1:indirect —— 被调是 gibbous-有-slot,DoCall 已压帧 + 把
//     被调帧 base 写中转字。本帧据 slot 经 `call_indirect <typeEntry> table0` 跨
//     module 直达被调 run(免 code.Run ~143ns 双跨层重入,PW10 R3 核心收益)。
//     被调 run 返 status:≠0 ⟹ 调 h_callerr 补弹遗留帧 + return 1;=0 ⟹ 从中转字
//     读刷新后的本帧 base(DoReturn 已写)续算。
//
// **base 刷新**:被调(回退同步路径 / R3 直调路径的 DoReturn)可能 growStack 使值栈
// 段在 arena 重定位,本帧 $base 失效。两路均刷新 base 后续算,免陈旧 base UAF。
func (c *Compiler) emitCall(em *emitter, proto *bytecode.Proto, ins bytecode.Instruction, pc int32) {
	a := int32(bytecode.A(ins))
	b := int32(bytecode.B(ins))
	cc := int32(bytecode.C(ins))
	xfer := c.host.CITransferAddr()

	// PW10 零跨界 ④ 守卫快路径(待 ④-ii 填守卫 + 帧建体):仅定额(B≠0 且 C≠0)
	// 尝试 Wasm 内建帧 + call_indirect 免 h_call 跨界。任一守卫失败 br $slow →
	// fallthrough 到下方 R3 indirect 慢路径(零行为变更回退)。④-i 阶段块体为空,
	// 守卫全过(空)即 fallthrough,行为等同直接走慢路径——纯结构占位,准备
	// ④-ii 填入守卫(tag/host/slot/vararg/needsArg/arity/MaxStack 余量)+ fast body
	// (段帧 4 word 写 + ciDepth++ + top 字 + call_indirect + 错误处理 + fastCallHits ++)。
	_ = proto // ④-ii 用 proto.MaxStack 作 caller 帧余量编译期常量
	fastEligible := b != 0 && cc != 0
	if fastEligible {
		em.block() // $slow: 守卫失败落点;块尾后接慢路径
		// ④-ii: emitCallFast(em, proto, a, b-1, cc-1) 在此发射
		em.end() // $slow 块尾,fallthrough 到下方 R3 indirect
	}

	// ret = h_call(base,pc,a,b,c)
	em.localGet(localBase)
	em.i32Const(pc)
	em.i32Const(a)
	em.i32Const(b)
	em.i32Const(cc)
	em.call(helperCall)
	em.localTee(localI64c)
	// ret < 0 → 错误冒泡
	em.i64Const(0)
	em.i64LtS()
	em.ifVoid()
	em.i32Const(1)
	em.ret()
	em.end()
	// 奇偶分派:ret & 1(奇 = indirect / 偶 = done)
	em.localGet(localI64c)
	em.i64Const(1)
	em.i64And()
	em.i32WrapI64()
	em.ifVoid()
	// --- 奇:call_indirect 直调 ---
	// 实参 calleeBase = i32.load(xfer)(DoCall 写的被调帧 base)
	em.i32Const(0)
	em.i32Load(xfer)
	// 表索引 slot = i32.wrap(ret >> 1)
	em.localGet(localI64c)
	em.i64Const(1)
	em.i64ShrU()
	em.i32WrapI64()
	em.callIndirect(typeEntry, 0)
	// status = 被调 run 返回值;≠0 → 补弹遗留帧 + 冒泡
	em.localTee(localI32)
	em.ifVoid()
	em.call(helperCallErr) // h_callerr:补弹遗留 gibbous 被调帧
	em.i32Const(1)
	em.ret()
	em.end()
	// OK:刷新本帧 base = i32.load(xfer)(DoReturn 已写刷新后 caller base)
	em.i32Const(0)
	em.i32Load(xfer)
	em.localSet(localBase)
	// PW10 零跨界 ③a:caller 自恢复 top(仅定额 C≠0)。直调返回后 th.top 须回到本帧
	// base+MaxStack(localSavedTop 快照,槽索引 grow 不变)。③a 阶段被调 helperReturn
	// 已恢复同值(幂等);③b 被调快路径不再恢复时此写回成唯一恢复点。C==0(多值到
	// top)不写——DoReturn 已设多值末尾 top,写回会破坏。
	//   (i32.store offset=topAddr (i32.const 0) (local.get $savedTop))
	if cc != 0 {
		em.i32Const(0)
		em.localGet(localSavedTop)
		em.i32Store(c.host.TopAddr())
	}
	em.elseOp()
	// --- 偶:done(回退同步跑完)刷新 base = i32.wrap(ret) ---
	em.localGet(localI64c)
	em.i32WrapI64()
	em.localSet(localBase)
	// PW10 零跨界 ③b:done 臂也自恢复 top(仅定额 C≠0)。done 臂可达「caller 是
	// gibbous、callee 无 slot 经 code.Run 同步跑完」——此时 callee 的 emitReturn 快
	// 路径(③b)按 G2(caller gibbous)走、跳过 top 恢复 → 须 F 在此自恢复。③a 下
	// DoReturn 已恢复同值(幂等);两臂对称,与 OK 臂同 savedTop 快照。
	if cc != 0 {
		em.i32Const(0)
		em.localGet(localSavedTop)
		em.i32Store(c.host.TopAddr())
	}
	em.end()
}

// emitTailCall TAILCALL A B C —— 尾调用复用帧(PW6-b,02 §3.6.2)。
//
//	(local.set $i32 (call h_tailcall(base,pc,a,b,c)))
//	(if (i32.eq $i32 1) (then (return 1)))      ;; ERR 冒泡
//	(if (i32.eq $i32 2) (then                   ;; host 尾调用:落尾随 RETURN
//	      (return (call h_return(base,pc,a,0))) ))
//	(return 0)                                  ;; Lua 尾调用完成,直接 return
//
// status 三态(gibbous_host.TailCall):0=Lua 尾调用已完成 / 1=ERR / 2=host(落 RETURN)。
// TAILCALL 是 BB 终结指令(无后继),自带 return,故 emitTailCall 自闭(不依赖
// 尾随 RETURN 指令——host 路径自调 h_return)。
func (c *Compiler) emitTailCall(em *emitter, ins bytecode.Instruction, pc int32) {
	a := int32(bytecode.A(ins))
	b := int32(bytecode.B(ins))
	cc := int32(bytecode.C(ins))
	em.localGet(localBase)
	em.i32Const(pc)
	em.i32Const(a)
	em.i32Const(b)
	em.i32Const(cc)
	em.call(helperTailCall)
	em.localSet(localI32)
	// status==1 → return 1(ERR)
	em.localGet(localI32)
	em.i32Const(1)
	em.i32Eq()
	em.ifVoid()
	em.i32Const(1)
	em.ret()
	em.end()
	// status==2 → host 尾调用:落尾随 RETURN A 0(到 top),return 其 status
	em.localGet(localI32)
	em.i32Const(2)
	em.i32Eq()
	em.ifVoid()
	em.localGet(localBase)
	em.i32Const(pc)
	em.i32Const(a)
	em.i32Const(0) // B=0:返回 R(A..top)(host 结果多值窗口)
	em.call(helperReturn)
	em.ret()
	em.end()
	// status==0 → Lua 尾调用已完成,直接 return 0
	em.i32Const(0)
	em.ret()
}
