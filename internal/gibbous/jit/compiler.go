//go:build wangshu_p4

// Package jit —— P4 编译器主体(wangshu_p4 build)。
//
// PJ0 阶段:SupportsAllOpcodes 全 false ⇒ 所有 Proto 仍走 crescent。
// PJ2 真接入版:Compile 识别「LOADK A K(0); RETURN A 1」最简形态,发射 mmap
// 段;p4Code.Run 经 callJITFull 拿 RAX 写回 R(A)——但 SupportsAllOpcodes
// **仍全 false** ⇒ bridge 不在主库路径触达 Compile,本路径仅由 PJ2 内部
// 单测 prove-the-path 走到(承 implementation-progress.md §6 PJ2 范围裁决)。
//
// 完整接入 crescent end-to-end byte-equal 留 PJ3+(SupportsAllOpcodes 开
// 白名单 + crescent.enterGibbousJIT 路径 + 配套 -race / difftest 验证)。
package jit

import (
	"errors"

	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
	jitamd64 "github.com/Liam0205/wangshu/internal/gibbous/jit/amd64"
	"github.com/Liam0205/wangshu/internal/value"
)

// Compiler 实现 `bridge.P3Compiler` 接口(`p2-bridge/05-p3-p4-interface.md`
// §2)。
type Compiler struct {
	// hostState 是注入的 host(crescent.State)抽象,供 p4Code.Run 弹帧。
	//
	// **per-Compiler 单例**(承 wireP4 单 goroutine 调用契约):每个 State
	// 一份 *Compiler,wireP4 时经 SetHostState 注入 *State;Compile 产 p4Code
	// 时把本字段复制到 p4Code.host;p4Code.Run 用自持的 host,与其它 State
	// 的 *p4Code 独立(无并发 write,V18 -race 友好)。
	hostState P4HostState

	// PJ3+ 字段位:
	//   - codePagePool *codePagePool  // exec mmap 代码页池(05 §2.1)
	//   - emitter      *amd64.Emitter // per-arch 发射器(06 §2.4)
	//   - state        *p4SpecState   // P4 投机子状态机(03 §4 方案 A)
	//
	// PJ2 留空(p4Code 自持 codePage,Compiler 状态 free)。
}

// New 构造 P4 Compiler。
func New() *Compiler {
	return &Compiler{}
}

// SupportsAllOpcodes 检查 Proto 中所有 opcode 是否都在后端支持集内。
//
// **PJ7 真接入实装**:开放白名单到「单值产生 + RETURN A 1」单 BB 形态——
// 这是 spike 闸门 ⊕ trampoline ⊕ emitter 三件套 + Go 端拆帧机制能 byte-equal
// 验证的 Lua 子集。
//
// 支持形态(必须满足:Code 长度 == 2,第二条 RETURN A 1):
//   - LOADK A K(Bx);RETURN A 1(常量返回,**含字符串常量**——
//     proto.Consts[bx] 已是 NaN-box GCRef,见 analyzeShape 字符串段注)
//   - LOADBOOL A B 0;RETURN A 1(bool 返回,C=0 不跳)
//   - LOADNIL A A;RETURN A 1(单 nil 返回,A==B)
//   - MOVE A B / GETUPVAL A B / ADD..POW A B C + RETURN A 2(详
//     analyzeShape)
//
// **关键**:常量族(LOADK/LOADBOOL/LOADNIL)共同点是「编译期能算出
// R(A) 的最终 NaN-box u64 值」(mmap 段只发 mov rax, imm64; ret);
// MOVE/GETUPVAL/算术族则借 Go 端 prelude 路径调 host helper 完成,mmap
// 段只是占位 trampoline。
//
// PJ8+ 启动时扩 supported(寄存器 IsNumber guard 投机 + 表 IC 直达槽等)
// 需要 jitContext load/store 值栈 + 投机 deopt 协议,留下一阶段。
func (c *Compiler) SupportsAllOpcodes(proto *bytecode.Proto) bool {
	return analyzeShape(proto).ok
}

// shapeInfo 是 analyzeShape 的返回值——P4 PJ7 形态识别结果。
type shapeInfo struct {
	ok         bool   // 形态合法
	retA       uint8  // RETURN A 寄存器号
	retB       uint8  // RETURN B 字段
	retPC      uint8  // RETURN 指令 pc
	value      uint64 // R(retA) 的 NaN-box u64 值(若 writeRetA=true 由 mmap 段烧入)
	writeRetA  bool   // mmap 段返 RAX 是否需写 R(retA)
	preludeOp  uint8  // RETURN 前 prelude opcode(0=无,GETUPVAL=4 / ADD=12 / SUB=13 等)
	preludeArg uint16 // prelude opcode 的 B 字段(GETUPVAL 的 upvalue 索引 0-255;算术族的 B 字段含 RK 编码 0-511)
	preludeC   uint16 // 算术族 prelude(ADD/SUB/MUL/DIV/MOD/POW)的 C 字段——可为 RK(常量含 256 偏移),0-511
}

// analyzeShape 识别支持的「单值产生 + RETURN A 1」形态。
//
// 支持形态:
//
//   - 长度 1:RETURN A 1/2(0 或 1 返回值)——R(A) 已是参数/Nil 槽
//   - 长度 2/3:LOADK/LOADBOOL/LOADNIL A ... + RETURN A 2(常量返,
//     writeRetA=true)
//   - 长度 2/3:首条 RETURN A 2(luac 优化形态,R(A) 已是参数)
//   - 长度 2/3:MOVE A B + RETURN A 2(等价 RETURN B 2,retA=B 跳过中转)
//   - 长度 2/3:GETUPVAL A B + RETURN A 2(Go 端 Run 调 host.GetUpval +
//     SetReg,preludeOp=GETUPVAL)
//   - 长度 2/3:ADD/SUB/MUL/DIV/MOD/POW A B C + RETURN A 2(Go 端 Run 调
//     host.Arith,逐字节同构解释器 doArith,preludeOp=算术 op,可 ERR 冒泡)
//   - 长度 2/3:UNM/LEN A B + RETURN A 2(Go 端 Run 调 host.Unm/Len,逐
//     字节同构解释器 UNM/LEN 慢路径,可 ERR 冒泡)
//   - 长度 2/3:NEWTABLE A B C + RETURN A 2(Go 端 Run 调 host.NewTable,
//     永不 raise——alloc + safepoint 全 helper 内)
//   - 长度 2/3:GETTABLE A B C + RETURN A 2(Go 端 Run 调 host.GetTable,
//     经 IC + 哈希 + __index 元方法链,可 ERR 冒泡)
func analyzeShape(proto *bytecode.Proto) shapeInfo {
	if proto == nil {
		return shapeInfo{}
	}

	// 形态 0:长度 1,RETURN A B(0 或 1 个返回值)
	if len(proto.Code) == 1 {
		ret := proto.Code[0]
		if bytecode.Op(ret) != bytecode.RETURN {
			return shapeInfo{}
		}
		retB := bytecode.B(ret)
		if retB != 1 && retB != 2 {
			return shapeInfo{}
		}
		return shapeInfo{ok: true, retA: uint8(bytecode.A(ret)), retB: uint8(retB), retPC: 0}
	}

	// 形态 1/2:长度 2 或 3
	if len(proto.Code) != 2 && len(proto.Code) != 3 {
		return shapeInfo{}
	}

	first := proto.Code[0]

	// 长度 3 时:第 3 条必须是 RETURN(尾部冗余)
	if len(proto.Code) == 3 {
		if bytecode.Op(proto.Code[2]) != bytecode.RETURN {
			return shapeInfo{}
		}
	}

	switch bytecode.Op(first) {
	case bytecode.RETURN:
		retA0 := bytecode.A(first)
		retB0 := bytecode.B(first)
		if retB0 != 1 && retB0 != 2 {
			return shapeInfo{}
		}
		return shapeInfo{ok: true, retA: uint8(retA0), retB: uint8(retB0), retPC: 0}

	case bytecode.MOVE:
		ret := proto.Code[1]
		if bytecode.Op(ret) != bytecode.RETURN {
			return shapeInfo{}
		}
		retA := bytecode.A(ret)
		retB := bytecode.B(ret)
		if retB != 2 {
			return shapeInfo{}
		}
		moveA := bytecode.A(first)
		moveB := bytecode.B(first)
		if moveA != retA {
			return shapeInfo{}
		}
		// retA 设为 B(直接返 R(B)),跳过 R(A) = R(B) 中转
		return shapeInfo{ok: true, retA: uint8(moveB), retB: uint8(retB), retPC: 1}

	case bytecode.GETUPVAL:
		// GETUPVAL A B + RETURN A 2:Run 调 host.GetUpval + SetReg。
		ret := proto.Code[1]
		if bytecode.Op(ret) != bytecode.RETURN {
			return shapeInfo{}
		}
		retA := bytecode.A(ret)
		retB := bytecode.B(ret)
		if retB != 2 {
			return shapeInfo{}
		}
		guvA := bytecode.A(first)
		guvB := bytecode.B(first)
		if guvA != retA {
			return shapeInfo{}
		}
		return shapeInfo{
			ok:         true,
			retA:       uint8(retA),
			retB:       uint8(retB),
			retPC:      1,
			preludeOp:  uint8(bytecode.GETUPVAL),
			preludeArg: uint16(guvB),
		}

	case bytecode.ADD, bytecode.SUB, bytecode.MUL, bytecode.DIV,
		bytecode.MOD, bytecode.POW:
		// ADD/SUB/MUL/DIV/MOD/POW A B C + RETURN A 2:Run 调 host.Arith 慢
		// 路径 helper(逐字节同构 doArith,含快路径再判 + 慢路径 coercion/
		// 元方法,可 raise)。本形态把「pure binop + 立即 return」典型形态
		// (`function(x, y) return x + y end` / `function(x) return x + 1 end`)
		// 接入 P4 升层,与 P3 同款"翻译走 helper"策略对位。
		ret := proto.Code[1]
		if bytecode.Op(ret) != bytecode.RETURN {
			return shapeInfo{}
		}
		retA := bytecode.A(ret)
		retB := bytecode.B(ret)
		if retB != 2 {
			return shapeInfo{}
		}
		arithA := bytecode.A(first)
		arithB := bytecode.B(first)
		arithC := bytecode.C(first)
		if arithA != retA {
			return shapeInfo{}
		}
		// RK 字段范围:B/C ∈ [0, 256) 是寄存器号,[256, 256+len(Consts)) 是
		// 常量索引(MaxK=256)。寄存器号上限 254(luac max stack),常量索引
		// 上限取决于 proto;无须额外校验—— host.Arith 复用解释器 reg/RK
		// 解析逻辑,越界时由 helper 自报错。
		if arithB > 511 || arithC > 511 { // 防御性:RK 最大编码 256+255=511
			return shapeInfo{}
		}
		return shapeInfo{
			ok:         true,
			retA:       uint8(retA),
			retB:       uint8(retB),
			retPC:      1,
			preludeOp:  uint8(bytecode.Op(first)),
			preludeArg: uint16(arithB),
			preludeC:   uint16(arithC),
		}

	case bytecode.UNM, bytecode.LEN:
		// UNM/LEN A B + RETURN A 2:一元运算族,B 是源寄存器号(无 RK 编码,
		// 取自 reg)。
		//
		//   - UNM:Run 调 host.Unm(逐字节同构解释器 UNM 慢路径,含 string
		//     coercion + __unm 元方法,可 raise);
		//   - LEN:Run 调 host.Len(string 字节长 / table border / table
		//     __len / 异类报错,可 raise)。
		//
		// **NOT 暂不支持**(`function(x) return not x end` 形态):NOT 需读
		// R(B) 取真假性 + setReg(A, BoolValue(!Truthy(b)))——当前 P4HostState
		// 接口无 GetReg 方法读寄存器,且新加 host helper(State.Not)又比纯
		// `not` 运算(Go 内 value.Truthy)重 N 倍。最干净裁决是「拒 NOT 形态,
		// 留 P3 / 解释器处理」——这是 P4 PJ7 简化形态根本约束:host 调用从
		// mmap 段移到 Go 端 Run,但需要的状态(R(B))Go 端拿不到。后续 PJ
		// 真接入 jitContext + GetReg 时一并扩。
		ret := proto.Code[1]
		if bytecode.Op(ret) != bytecode.RETURN {
			return shapeInfo{}
		}
		retA := bytecode.A(ret)
		retB := bytecode.B(ret)
		if retB != 2 {
			return shapeInfo{}
		}
		uA := bytecode.A(first)
		uB := bytecode.B(first)
		if uA != retA {
			return shapeInfo{}
		}
		// UNM/LEN 的 B 是寄存器号,取值范围 [0, 254]
		if uB > 254 {
			return shapeInfo{}
		}
		return shapeInfo{
			ok:         true,
			retA:       uint8(retA),
			retB:       uint8(retB),
			retPC:      1,
			preludeOp:  uint8(bytecode.Op(first)),
			preludeArg: uint16(uB),
		}

	case bytecode.NEWTABLE:
		// NEWTABLE A B C + RETURN A 2:`function() return {} end` /
		// `function() return {1,2,3} end`(单 NEWTABLE 形态,后者还需 SETLIST
		// 不在本简化形态)。host.NewTable 永不 raise(alloc + safepoint
		// 全 helper 内,Go runtime OOM 才崩),与算术族的可 raise 路径不同。
		ret := proto.Code[1]
		if bytecode.Op(ret) != bytecode.RETURN {
			return shapeInfo{}
		}
		retA := bytecode.A(ret)
		retB := bytecode.B(ret)
		if retB != 2 {
			return shapeInfo{}
		}
		ntA := bytecode.A(first)
		ntB := bytecode.B(first)
		ntC := bytecode.C(first)
		if ntA != retA {
			return shapeInfo{}
		}
		// NEWTABLE B/C 是 Fb 编码的初始大小提示,范围 [0, 255]
		if ntB > 255 || ntC > 255 {
			return shapeInfo{}
		}
		return shapeInfo{
			ok:         true,
			retA:       uint8(retA),
			retB:       uint8(retB),
			retPC:      1,
			preludeOp:  uint8(bytecode.NEWTABLE),
			preludeArg: uint16(ntB),
			preludeC:   uint16(ntC),
		}

	case bytecode.GETTABLE:
		// GETTABLE A B C + RETURN A 2:`function(t, k) return t[k] end` /
		// `function(t) return t[1] end` 形态(C 可为 RK 编码)。host.GetTable
		// 走 IC + 哈希 + __index 元方法链,可 raise(attempt to index nil 等)。
		ret := proto.Code[1]
		if bytecode.Op(ret) != bytecode.RETURN {
			return shapeInfo{}
		}
		retA := bytecode.A(ret)
		retB := bytecode.B(ret)
		if retB != 2 {
			return shapeInfo{}
		}
		gtA := bytecode.A(first)
		gtB := bytecode.B(first)
		gtC := bytecode.C(first)
		if gtA != retA {
			return shapeInfo{}
		}
		// B 是寄存器号(表对象);C 是 RK 编码(键),取值上限 511
		if gtB > 254 || gtC > 511 {
			return shapeInfo{}
		}
		return shapeInfo{
			ok:         true,
			retA:       uint8(retA),
			retB:       uint8(retB),
			retPC:      1,
			preludeOp:  uint8(bytecode.GETTABLE),
			preludeArg: uint16(gtB),
			preludeC:   uint16(gtC),
		}

	case bytecode.LOADK, bytecode.LOADBOOL, bytecode.LOADNIL:
		ret := proto.Code[1]
		if bytecode.Op(ret) != bytecode.RETURN {
			return shapeInfo{}
		}
		retA := bytecode.A(ret)
		retB := bytecode.B(ret)
		if retB != 2 {
			return shapeInfo{}
		}

		switch bytecode.Op(first) {
		case bytecode.LOADK:
			loadA := bytecode.A(first)
			loadBx := bytecode.Bx(first)
			if loadA != retA {
				return shapeInfo{}
			}
			if loadBx < 0 || loadBx >= len(proto.Consts) {
				return shapeInfo{}
			}
			// LOADK 字符串常量 OK:`proto.Consts[bx]` 在 State 私有 Proto 上
			// 已是 NaN-box `MakeGC(TagString, intern_ref)`(State.LoadProgram
			// 经 gc.Intern 写入,见 state.go::LoadProgram §私有 Consts 段)。
			// 只要 p4Code 持 proto 指针,proto.Consts 是 GC 根的一部分,
			// string ref 永远活——与 number/nil/bool 三档同源,直接发 mov
			// rax, imm64; ret 即可。
			return shapeInfo{
				ok: true, retA: uint8(retA), retB: uint8(retB), retPC: 1,
				value: uint64(proto.Consts[loadBx]), writeRetA: true,
			}

		case bytecode.LOADBOOL:
			loadA := bytecode.A(first)
			loadB := bytecode.B(first)
			loadC := bytecode.C(first)
			if loadA != retA {
				return shapeInfo{}
			}
			if loadC != 0 {
				return shapeInfo{}
			}
			var v value.Value
			if loadB != 0 {
				v = value.BoolValue(true)
			} else {
				v = value.BoolValue(false)
			}
			return shapeInfo{
				ok: true, retA: uint8(retA), retB: uint8(retB), retPC: 1,
				value: uint64(v), writeRetA: true,
			}

		case bytecode.LOADNIL:
			loadA := bytecode.A(first)
			loadB := bytecode.B(first)
			if loadA != retA || loadA != loadB {
				return shapeInfo{}
			}
			return shapeInfo{
				ok: true, retA: uint8(retA), retB: uint8(retB), retPC: 1,
				value: uint64(value.Nil), writeRetA: true,
			}
		}
	}
	return shapeInfo{}
}

// Compile 把 Proto 编译成 GibbousCode(可执行产物)。
//
// **PJ7 真接入实装**(扩展版):识别「单值产生 + RETURN A 1」单 BB 形态——
// LOADK / LOADBOOL / LOADNIL 三档(承 analyzeShape):
//  1. 经 analyzeShape 算出 retA + value(NaN-box u64);
//  2. emitter 发射 `mov rax, value; ret`(11 字节);
//  3. mmap PROT_RW + 写码 + mprotect PROT_RX(承 05 §2.1);
//  4. 包装 *p4Code(retA + host = c.hostState 拷贝)。
//
// 其它形态返 ErrCompileUnsupportedShape(承
// `p2-bridge/05-p3-p4-interface.md` §2.2.2 错误返回语义)——bridge 收到错误
// 后把该 Proto 标 TierStuck(永久解释,不重试)。
func (c *Compiler) Compile(proto *bytecode.Proto, feedback *bridge.TypeFeedback) (bridge.GibbousCode, error) {
	_ = feedback
	info := analyzeShape(proto)
	if !info.ok {
		return nil, ErrCompileUnsupportedShape
	}

	// 发射:mov rax, val; ret(emitter 内已在 PJ1 实装)。
	// writeRetA=false 时 val 不被使用(mmap 段 RAX 是 dummy);仍发 mov+ret
	// 因为 mmap 段必须非空。
	var buf []byte
	buf = jitamd64.EmitMovRaxImm64(buf, info.value)
	buf = jitamd64.EmitRet(buf)

	page, err := jitamd64.MmapCode(buf)
	if err != nil {
		return nil, err
	}

	return &p4Code{
		proto:      proto,
		codePage:   page,
		jitCtx:     NewJITContext(),
		retA:       info.retA,
		retB:       info.retB,
		retPC:      info.retPC,
		writeRetA:  info.writeRetA,
		preludeOp:  info.preludeOp,
		preludeArg: info.preludeArg,
		preludeC:   info.preludeC,
		host:       c.hostState,
	}, nil
}

// ErrCompileUnsupportedShape:Compile 拒绝 Proto 形态不在 PJ7 真接入子集的
// 兜底返错——SupportsAllOpcodes 已在 F7 拦下绝大多数,本错误是 jit 包内
// prove-the-path 单测路径绕过 SupportsAllOpcodes 直调 Compile 时的二次形态
// 检查兜底。bridge 收到本错误把该 Proto 标 TierStuck(永久解释,不重试)。
//
// PJ7 真接入支持形态:
//   - 长度 1:RETURN A B(B=1 空函数 / B=2 identity 返参数)
//   - 长度 2/3:首条 RETURN A 2(luac 优化形态)
//   - 长度 2/3:MOVE A B + RETURN A 2(retA=B 跳过中转)
//   - 长度 2/3:GETUPVAL A B + RETURN A 2(prelude 路径调 host.GetUpval)
//   - 长度 2/3:LOADK/LOADBOOL/LOADNIL A ... + RETURN A 2(常量返)
//   - 长度 2/3:ADD/SUB/MUL/DIV/MOD/POW A B C + RETURN A 2(prelude 路径
//     调 host.Arith 慢路径 helper,可 ERR 冒泡)
//   - 长度 2/3:UNM/LEN A B + RETURN A 2(prelude 路径调 host.Unm/Len
//     慢路径 helper,可 ERR 冒泡)
//   - 长度 2/3:NEWTABLE A B C + RETURN A 2(prelude 路径调 host.NewTable,
//     永不 raise)
//   - 长度 2/3:GETTABLE A B C + RETURN A 2(prelude 路径调 host.GetTable,
//     经 IC + __index 元方法链,可 ERR 冒泡)
var ErrCompileUnsupportedShape = errors.New("internal/gibbous/jit: P4 PJ7 unsupported shape (expected: single RETURN A B / single-BB MOVE|GETUPVAL|LOADK|LOADBOOL|LOADNIL|ADD..POW|UNM|LEN|NEWTABLE|GETTABLE + RETURN A 2)")
