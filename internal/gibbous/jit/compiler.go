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
	preludeOp  uint8  // RETURN 前 prelude opcode(0=无,GETUPVAL=4 / ADD=12 / SUB=13 / GETGLOBAL=5 / SETGLOBAL=7 / SETTABLE=9 等)
	preludeArg uint32 // prelude opcode 的 B 字段(GETUPVAL/UNM/LEN 是寄存器号 0-255;算术族 B 是 RK 0-511;NEWTABLE B 是 Fb 0-255;GETGLOBAL/SETGLOBAL 是 Bx 0-262143,需 18-bit)
	preludeC   uint16 // 算术族 / 表族 prelude 的 C 字段——可为 RK(常量含 256 偏移),0-511
	cmpA       uint8  // 比较折叠形态:EQ/LT/LE 的 A 字段(0=结果取反 / 1=直接取结果,用于折成 BoolValue(packed.bit0 == cmpA))
	// 二段算术链式形态(MUL+ADD+RETURN 等):第二段算术 op + B + C
	chainOp uint8  // 第二段 op(0=无 chain;ADD/SUB/MUL/DIV/MOD/POW)
	chainB  uint16 // 第二段 B 字段(RK 0-511)
	chainC  uint16 // 第二段 C 字段(RK 0-511)

	// PJ3 FORLOOP 字节级 inline 形态识别(空 body / 全常量 init/limit/step):
	//   - isForLoop = true:本 shape 是 FORLOOP 形态,Compile 走 emit FORLOOP
	//     模板(浮点 idx+=step / ucomisd limit / backward jcc)路径
	//   - forA:FORPREP/FORLOOP 的 A 字段(R(A)..R(A+3) 是 idx/limit/step/i)。
	//     **当前空 body 形态 emit 不读 forA**(模板只用 forInitK/forLimitK/
	//     forStepK 烧入 imm64,不寻址 R(A) 槽);**留 PJ3+ body inline 扩**
	//     时需用 forA 算 R(A+3)=i 槽 offset 给 body 内部 ref。
	//   - forInitK / forLimitK / forStepK:三个常量 NaN-box raw bits(编译期烧 imm64)
	isForLoop bool
	forA      uint8
	forInitK  uint64
	forLimitK uint64
	forStepK  uint64
}

// analyzeForLoopForm 识别 PJ3 字节级 FORLOOP inline 最简形态:
// `function() for i=K1, K2 do end end`(全常量 init/limit/step + 空 body)。
//
// luac 编码(以 `for i=1,100 do end` 为例,假设无外部 local):
//
//	[0] LOADK    A   -kInit  ; R(A)=init = K[kInit]
//	[1] LOADK    A+1 -kLimit ; R(A+1)=limit = K[kLimit]
//	[2] LOADK    A+2 -kStep  ; R(A+2)=step = K[kStep]
//	[3] FORPREP  A   sBx=0   ; R(A)-=step;jmp 到 FORLOOP
//	[4] FORLOOP  A   sBx=-1  ; R(A)+=step;cmp limit;jmp 回 [4](空 body)
//	[5] RETURN   0   1       ; 空 return
//	[6] RETURN   0   1       ; (可选 dead RETURN,luac 主 chunk 尾部)
//
// **形态约束**(为字节级模板简化,只接最简):
//   - proto.Code 长度 6 或 7(尾部可选 dead RETURN)
//   - [0] LOADK A_init -kInit
//   - [1] LOADK A_init+1 -kLimit
//   - [2] LOADK A_init+2 -kStep
//   - [3] FORPREP A_init sBx=0(空 body 时 luac 编 0)
//   - [4] FORLOOP A_init sBx=-1(回边跳自己)
//   - [5] RETURN A=0 B=1(空 return)
//   - K[kInit / kLimit / kStep] 必须都是 number(否则降级 host)
//
// **不支持**(留 PJ3 真接入扩展):
//   - body 非空(需 inline body opcodes + 寄存器分配)
//   - limit 是参数 reg(`for i=1,n do`)— 需 IsNumber guard
//   - 嵌套 for / 含 break(JMP)
//   - 非默认 step(step=1 隐含;非默认编码 step 仍走本路径,因 step 也是 K)
func analyzeForLoopForm(proto *bytecode.Proto) (shapeInfo, bool) {
	codeLen := len(proto.Code)
	if codeLen != 6 && codeLen != 7 {
		return shapeInfo{}, false
	}
	// [0/1/2] LOADK / [3] FORPREP / [4] FORLOOP / [5] RETURN
	// **limit 仅 LOADK 形态**(MOVE/reg 形态留 PJ3+ 加 IsNumber guard 时扩)
	if bytecode.Op(proto.Code[0]) != bytecode.LOADK ||
		bytecode.Op(proto.Code[1]) != bytecode.LOADK ||
		bytecode.Op(proto.Code[2]) != bytecode.LOADK ||
		bytecode.Op(proto.Code[3]) != bytecode.FORPREP ||
		bytecode.Op(proto.Code[4]) != bytecode.FORLOOP ||
		bytecode.Op(proto.Code[5]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	if codeLen == 7 && bytecode.Op(proto.Code[6]) != bytecode.RETURN {
		return shapeInfo{}, false
	}

	// A 字段一致
	aInit := bytecode.A(proto.Code[0])
	aLimit := bytecode.A(proto.Code[1])
	aStep := bytecode.A(proto.Code[2])
	aPrep := bytecode.A(proto.Code[3])
	aLoop := bytecode.A(proto.Code[4])
	if aLimit != aInit+1 || aStep != aInit+2 || aPrep != aInit || aLoop != aInit {
		return shapeInfo{}, false
	}

	// FORPREP sBx == 0, FORLOOP sBx == -1
	if bytecode.SBx(proto.Code[3]) != 0 || bytecode.SBx(proto.Code[4]) != -1 {
		return shapeInfo{}, false
	}

	// RETURN A=0 B=1
	if bytecode.A(proto.Code[5]) != 0 || bytecode.B(proto.Code[5]) != 1 {
		return shapeInfo{}, false
	}

	// 三个 LOADK 的 Bx 取 K[Bx]
	kInitIdx := bytecode.Bx(proto.Code[0])
	kLimitIdx := bytecode.Bx(proto.Code[1])
	kStepIdx := bytecode.Bx(proto.Code[2])
	if kInitIdx >= len(proto.Consts) || kLimitIdx >= len(proto.Consts) || kStepIdx >= len(proto.Consts) {
		return shapeInfo{}, false
	}

	kInit := proto.Consts[kInitIdx]
	kLimit := proto.Consts[kLimitIdx]
	kStep := proto.Consts[kStepIdx]
	if !value.IsNumber(kInit) || !value.IsNumber(kLimit) || !value.IsNumber(kStep) {
		return shapeInfo{}, false
	}

	// **step > 0 才支持本简化模板**(jcc 选 ja:idx > limit 退出)。
	// step ≤ 0 或负 step 留 PJ3+ 扩(jcc 选 jb:idx < limit 退出)。
	stepF := value.AsNumber(kStep)
	if stepF <= 0 {
		return shapeInfo{}, false
	}

	return shapeInfo{
		ok:        true,
		retA:      0, // RETURN A=0
		retB:      1, // 空 return
		retPC:     5,
		isForLoop: true,
		forA:      uint8(aInit),
		forInitK:  uint64(kInit),
		forLimitK: uint64(kLimit),
		forStepK:  uint64(kStep),
	}, true
}

// analyzeCompareForm 识别 EQ/LT/LE + JMP + LOADBOOL + LOADBOOL + RETURN
// (+ dead RETURN)折叠形态(`function(x) return x == 1 end` 类)。
//
// luac 编码(以 EQ 为例):
//
//	[0] EQ        A=cmpA B C    (cmpA=1:跳过下一条当 R(B)==RK(C);cmpA=0:反之)
//	[1] JMP       A=0 sBx=1     (跳到 LOADBOOL true,即 [3])
//	[2] LOADBOOL  A=retA B=0 C=1 (false + 跳过下一条;不到此处则下一条跑)
//	[3] LOADBOOL  A=retA B=1 C=0 (true)
//	[4] RETURN    A=retA B=2
//	[5] RETURN    A=0 B=1       (dead,可选尾部冗余)
//
// 等价语义:`R(retA) = BoolValue(cmp(B,C) == (cmpA==1))`(packed bit0 与
// cmpA 比较,值相等即返回 true)。Run 路径调 host.Compare(B, C) 拿
// packed 后,折成 BoolValue 经 SetReg 写 R(retA)。
//
// 支持 EQ(23)/LT(24)/LE(25) 三个比较 op。
func analyzeCompareForm(proto *bytecode.Proto) (shapeInfo, bool) {
	if len(proto.Code) != 5 && len(proto.Code) != 6 {
		return shapeInfo{}, false
	}

	cmp := proto.Code[0]
	jmp := proto.Code[1]
	lbFalse := proto.Code[2]
	lbTrue := proto.Code[3]
	ret := proto.Code[4]

	// op 0:EQ/LT/LE
	cmpOp := bytecode.Op(cmp)
	if cmpOp != bytecode.EQ && cmpOp != bytecode.LT && cmpOp != bytecode.LE {
		return shapeInfo{}, false
	}
	cmpA := bytecode.A(cmp)
	cmpB := bytecode.B(cmp)
	cmpC := bytecode.C(cmp)
	if cmpA != 0 && cmpA != 1 {
		return shapeInfo{}, false
	}
	if cmpB > 511 || cmpC > 511 {
		return shapeInfo{}, false
	}

	// op 1:JMP sBx=1(跳过下一条)
	if bytecode.Op(jmp) != bytecode.JMP {
		return shapeInfo{}, false
	}
	if bytecode.SBx(jmp) != 1 {
		return shapeInfo{}, false
	}

	// op 2:LOADBOOL A=retA B=0 C=1(false + 跳过下一条)
	if bytecode.Op(lbFalse) != bytecode.LOADBOOL {
		return shapeInfo{}, false
	}
	lbFalseA := bytecode.A(lbFalse)
	if bytecode.B(lbFalse) != 0 || bytecode.C(lbFalse) != 1 {
		return shapeInfo{}, false
	}

	// op 3:LOADBOOL A=retA B=1 C=0(true)
	if bytecode.Op(lbTrue) != bytecode.LOADBOOL {
		return shapeInfo{}, false
	}
	lbTrueA := bytecode.A(lbTrue)
	if lbTrueA != lbFalseA {
		return shapeInfo{}, false
	}
	if bytecode.B(lbTrue) != 1 || bytecode.C(lbTrue) != 0 {
		return shapeInfo{}, false
	}

	// op 4:RETURN A=retA B=2
	if bytecode.Op(ret) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	retA := bytecode.A(ret)
	retB := bytecode.B(ret)
	if retA != lbTrueA || retB != 2 {
		return shapeInfo{}, false
	}

	// op 5:可选 dead RETURN(B=1)
	if len(proto.Code) == 6 {
		if bytecode.Op(proto.Code[5]) != bytecode.RETURN {
			return shapeInfo{}, false
		}
	}

	return shapeInfo{
		ok:         true,
		retA:       uint8(retA),
		retB:       uint8(retB),
		retPC:      4, // RETURN 在 pc 4
		preludeOp:  uint8(cmpOp),
		preludeArg: uint32(cmpB),
		preludeC:   uint16(cmpC),
		cmpA:       uint8(cmpA),
	}, true
}

// analyzeArithChainForm 识别二段算术链式形态(`function(x) return x*2+1 end`
// 类),长度 3 或 4:
//
//	[0] arith1 A B C    (ADD/SUB/MUL/DIV/MOD/POW;A 不一定 = retA,但 A 必须
//	                     是 arith2 的 B 输入位置)
//	[1] arith2 A B C    (B = arith1.A,链式输入;A 一致 retA)
//	[2] RETURN A 2
//	[3] dead RETURN(可选)
//
// 等价语义:Run 串行调 host.Arith(op1, B1, C1, A1)再调 host.Arith(op2,
// B2=A1, C2, A2)——中间值经 ci 的 reg 槽自然传递,与解释器执行同源。
//
// **关键约束**:arith1.A 必须 == arith2.B(链式输入,且 luac 编码后两 op
// 的 A 同 retA)。本简化只接 op1.A == op2.A == retA 形态(luac 默认产物)。
func analyzeArithChainForm(proto *bytecode.Proto) (shapeInfo, bool) {
	if len(proto.Code) != 3 && len(proto.Code) != 4 {
		return shapeInfo{}, false
	}
	op1 := proto.Code[0]
	op2 := proto.Code[1]
	ret := proto.Code[2]

	isArith := func(op bytecode.OpCode) bool {
		return op == bytecode.ADD || op == bytecode.SUB || op == bytecode.MUL ||
			op == bytecode.DIV || op == bytecode.MOD || op == bytecode.POW
	}
	if !isArith(bytecode.Op(op1)) || !isArith(bytecode.Op(op2)) {
		return shapeInfo{}, false
	}
	if bytecode.Op(ret) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	retA := bytecode.A(ret)
	retB := bytecode.B(ret)
	if retB != 2 {
		return shapeInfo{}, false
	}

	// op1: A B C; op2: A B C
	op1A := bytecode.A(op1)
	op2A := bytecode.A(op2)
	op2B := bytecode.B(op2)
	if op1A != retA || op2A != retA {
		return shapeInfo{}, false
	}
	// op2.B 必须读 op1 的输出(=op1.A=retA)——chain 链式输入
	if op2B != retA {
		return shapeInfo{}, false
	}

	op1B := bytecode.B(op1)
	op1C := bytecode.C(op1)
	op2C := bytecode.C(op2)
	if op1B > 511 || op1C > 511 || op2C > 511 {
		return shapeInfo{}, false
	}

	// 长度 4 时 [3] 必须是 dead RETURN
	if len(proto.Code) == 4 {
		if bytecode.Op(proto.Code[3]) != bytecode.RETURN {
			return shapeInfo{}, false
		}
	}

	return shapeInfo{
		ok:         true,
		retA:       uint8(retA),
		retB:       uint8(retB),
		retPC:      2, // RETURN 在 pc 2
		preludeOp:  uint8(bytecode.Op(op1)),
		preludeArg: uint32(op1B),
		preludeC:   uint16(op1C),
		chainOp:    uint8(bytecode.Op(op2)),
		chainB:     uint16(op2B), // = retA(链式)
		chainC:     uint16(op2C),
	}, true
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
//   - **长度 3/4 二段算术链式**:arith1 A B C + arith2 A A C2 + RETURN A 2
//     (`function(x) return x*2+1 end` 类——MUL+ADD+RETURN)。Run 串行调
//     host.Arith 两次,中间值在 R(A)。
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
		// 长度 5/6:可能是比较折叠形态 EQ/LT/LE+JMP+LOADBOOL+LOADBOOL+RETURN(+RETURN)
		if cmp, ok := analyzeCompareForm(proto); ok {
			return cmp
		}
		// 长度 3/4:可能是二段算术链式形态(arith1 + arith2 + RETURN [+dead])
		if chain, ok := analyzeArithChainForm(proto); ok {
			return chain
		}
		// 长度 6/7:可能是 PJ3 FORLOOP 空 body 全常量形态
		if floop, ok := analyzeForLoopForm(proto); ok {
			return floop
		}
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
		// B 是寄存器号 [0,254](与 GETTABLE/UNM/LEN 等寄存器号 case 一致防御),
		// luac MAXSTACK 上限 250 实际不触发,纯防御性兜底。
		if moveB > 254 {
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
			preludeArg: uint32(guvB),
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
			preludeArg: uint32(arithB),
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
			preludeArg: uint32(uB),
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
			preludeArg: uint32(ntB),
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
			preludeArg: uint32(gtB),
			preludeC:   uint16(gtC),
		}

	case bytecode.GETGLOBAL:
		// GETGLOBAL A Bx + RETURN A 2:`function() return print end` 形态。
		// host.DoGetGlobal 经 icGetTable 在 `_G` 上查 Consts[bx],可 raise
		// (元方法路径)。
		ret := proto.Code[1]
		if bytecode.Op(ret) != bytecode.RETURN {
			return shapeInfo{}
		}
		retA := bytecode.A(ret)
		retB := bytecode.B(ret)
		if retB != 2 {
			return shapeInfo{}
		}
		ggA := bytecode.A(first)
		ggBx := bytecode.Bx(first)
		if ggA != retA {
			return shapeInfo{}
		}
		// Bx 18-bit, [0, 262143] —— 须存进 preludeArg (uint32)
		if ggBx < 0 || ggBx > 262143 {
			return shapeInfo{}
		}
		if ggBx >= len(proto.Consts) {
			return shapeInfo{}
		}
		return shapeInfo{
			ok:         true,
			retA:       uint8(retA),
			retB:       uint8(retB),
			retPC:      1,
			preludeOp:  uint8(bytecode.GETGLOBAL),
			preludeArg: uint32(ggBx),
		}

	case bytecode.SETGLOBAL:
		// SETGLOBAL A Bx + RETURN A 1:setter 形态(0 返回值)。
		// `function() x = 1 end` 编译为 LOADK + SETGLOBAL + RETURN(长度 3),
		// 故识别 SETGLOBAL 作 prelude 需要前置 LOADK 已写入 R(A)——这违反
		// 「单 prelude op + RETURN」简化形态。**SETGLOBAL 形态由 LOADK
		// prelude 覆盖不到,本档暂不接**——需要多 prelude 链(LOADK + SETGLOBAL
		// 双 op + RETURN)留下一档扩展。这里仅处理「源已在 R(A) 的简化形态」
		// (实践中罕见),配合 retB=1 setter 守卫。
		ret := proto.Code[1]
		if bytecode.Op(ret) != bytecode.RETURN {
			return shapeInfo{}
		}
		retB := bytecode.B(ret)
		if retB != 1 { // setter 必须 0 返回值
			return shapeInfo{}
		}
		sgA := bytecode.A(first)
		sgBx := bytecode.Bx(first)
		if sgBx < 0 || sgBx > 262143 {
			return shapeInfo{}
		}
		if sgBx >= len(proto.Consts) {
			return shapeInfo{}
		}
		return shapeInfo{
			ok:         true,
			retA:       uint8(sgA),
			retB:       uint8(retB),
			retPC:      1,
			preludeOp:  uint8(bytecode.SETGLOBAL),
			preludeArg: uint32(sgBx),
		}

	case bytecode.SETTABLE:
		// SETTABLE A B C + RETURN A 1:`function(t,k,v) t[k]=v end` 形态。
		// host.SetTable 经 icSetTable IC + 哈希 + __newindex,可 raise。
		// **setter 形态 retB=1**(0 返回值),不写 R(A)。
		ret := proto.Code[1]
		if bytecode.Op(ret) != bytecode.RETURN {
			return shapeInfo{}
		}
		retB := bytecode.B(ret)
		if retB != 1 { // setter 必须 0 返回值
			return shapeInfo{}
		}
		stA := bytecode.A(first)
		stB := bytecode.B(first)
		stC := bytecode.C(first)
		// A 是表寄存器号 [0,254];B/C 是 RK [0,511]
		if stA > 254 || stB > 511 || stC > 511 {
			return shapeInfo{}
		}
		return shapeInfo{
			ok:         true,
			retA:       uint8(stA),
			retB:       uint8(retB),
			retPC:      1,
			preludeOp:  uint8(bytecode.SETTABLE),
			preludeArg: uint32(stB),
			preludeC:   uint16(stC),
		}

	case bytecode.SETUPVAL:
		// SETUPVAL A B + RETURN A 1:`function(v) upval = v end` 形态,
		// setter 0 返回值。host.SetUpvalFromReg 经 reg(A) 读源 + upvalSet
		// 写 upvalue。永不 raise。
		ret := proto.Code[1]
		if bytecode.Op(ret) != bytecode.RETURN {
			return shapeInfo{}
		}
		retB := bytecode.B(ret)
		if retB != 1 { // setter 必须 0 返回值
			return shapeInfo{}
		}
		suvA := bytecode.A(first)
		suvB := bytecode.B(first)
		// A 是源寄存器 [0,254];B 是 upvalue 索引 [0,255]
		if suvA > 254 || suvB > 255 {
			return shapeInfo{}
		}
		return shapeInfo{
			ok:         true,
			retA:       uint8(suvA),
			retB:       uint8(retB),
			retPC:      1,
			preludeOp:  uint8(bytecode.SETUPVAL),
			preludeArg: uint32(suvB),
		}

	case bytecode.NOT:
		// NOT A B + RETURN A 2:`function(x) return not x end` 形态。
		// 纯 Truthy 逻辑(无 metamethod、无 raise),Run 直接经 host.GetReg
		// 读 R(B) + SetReg(A, BoolValue(!Truthy(...))),不调 host helper
		// 完成算术(GetReg/SetReg 走 host 接口是因为 jit 不能直接访问 arena)。
		ret := proto.Code[1]
		if bytecode.Op(ret) != bytecode.RETURN {
			return shapeInfo{}
		}
		retA := bytecode.A(ret)
		retB := bytecode.B(ret)
		if retB != 2 {
			return shapeInfo{}
		}
		notA := bytecode.A(first)
		notB := bytecode.B(first)
		if notA != retA {
			return shapeInfo{}
		}
		if notB > 254 {
			return shapeInfo{}
		}
		return shapeInfo{
			ok:         true,
			retA:       uint8(retA),
			retB:       uint8(retB),
			retPC:      1,
			preludeOp:  uint8(bytecode.NOT),
			preludeArg: uint32(notB),
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
			// **GC 根保活**:string ref 由 `State.strRefs`(R6 根)经
			// LoadProgram 注册,经 visitProgramStringRefs 扫到 collector;
			// proto.Consts 自身**不**被当作根遍历,p4Code 持 proto 指针只
			// 是间接保 proto 活,不是 string ref 保活的机制。但实际效果一致
			// (LoadProgram 注册的 strRefs 与 proto 同生命期),mmap 烧入的
			// NaN-box u64 在程序加载期间安全。
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
// **PJ7 真接入实装**:识别 analyzeShape 支持的单 BB 形态(getter/setter/
// 比较折叠 ~25 类——见 analyzeShape godoc 完整清单 + ErrCompileUnsupportedShape
// 单档列):
//  1. 经 analyzeShape 算出 retA/retB/preludeOp/value/cmpA/...;
//  2. emitter 发射 `mov rax, value; ret`(11 字节,常量族烧 NaN-box,
//     prelude/比较折叠族 RAX 是 dummy 由 Run 端忽略);
//  3. mmap PROT_RW + 写码 + mprotect PROT_RX(承 05 §2.1);
//  4. 包装 *p4Code(retA + 各 prelude 字段 + host = c.hostState 拷贝)。
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

	// **PJ3 FORLOOP 字节级 inline 真接入**(承 05 §6.3 + 06 §3.3):
	// 全常量 init/limit/step + 空 body FORLOOP 形态(`for i=1,K do end`)走
	// 字节级 FORLOOP 模板——69 字节 mmap+RX 段内自循环,完整段内 idx+=step
	// + ucomisd limit + backward jmp,无外部副作用,空 body 不需写 R(A)..
	//
	// **mock host 兜底**:同 PJ2 路径,host.ArenaBaseAddr=0 时降级——但
	// 空 body FORLOOP 完全无寻址(模板不读 rbx),mock 路径也可启用。为统一
	// 接入规约,仍按 PJ2 同款 mock host 守卫处理。
	if info.isForLoop && archSupportsSpec() &&
		c.hostState != nil && c.hostState.ArenaBaseAddr() != 0 {
		var buf []byte
		// safepoint check 接入 — preemptFlag 字段偏移传给模板,模板在
		// loop body 末尾插「cmp byte [r15+pfOff], 0; jne after_loop」
		// (承 05 §1.2.2 抢占纪律 + V18 -race);trampoline 已装 r15。
		pfOff := int32(JITContextPreemptFlagOffset)
		buf = archEmitForLoopEmptyConst(buf, info.forInitK, info.forLimitK, info.forStepK, pfOff)
		page, err := archMmapCode(buf)
		if err != nil {
			return nil, err
		}
		incSpecForLoopHits() // prove-the-path 白盒命中证据
		return &p4Code{
			proto:    proto,
			codePage: page,
			jitCtx:   NewJITContext(),
			retA:     info.retA,
			retB:     info.retB, // 1 = 空 return
			retPC:    info.retPC,
			// 空 body FORLOOP 不写 R(A) 任何槽;writeRetA=false + preludeOp=0
			// → Run 路径不走 prelude switch + 不写 RAX,只调 DoReturn 弹帧
			writeRetA: false,
			host:      c.hostState,
			// useSpec=false 走 archCallJITFull(段内自循环,完整 trampoline
			// 装 r15 不必需但 OK——模板不读 r15)
			useSpec: false,
		}, nil
	}

	// **PJ2 投机算术模板真接入**(承 03-speculation-ic.md §2 IsNumber×2):
	// 当且仅当本 arch 支持(amd64)+ ADD/SUB/MUL/DIV A B C + RETURN A 2
	// 形态 + 真 host(非 mock,ArenaBaseAddr 非 0)时,emit 投机模板。
	//
	// 操作数形态分流(承 ../bytecode/instruction.go RK 编码):
	//   - **reg-reg**(B/C ≤ 254 都是寄存器):92 字节模板,IsNumber guard×2
	//     + 双 number 快路径(movsd+<sseOp>+movsd+ret)+ deopt block;
	//   - **reg-K**(B ≤ 254 reg + C ≥ 256 是常量索引,K[c-256] 必须是
	//     number):73 字节模板,单 guard reg 端 + 烧 K 值 imm64 + 快路径
	//     + deopt block;K 端编译期已校验为 number,运行期不再 guard。
	// Run 检测段返 RAX == specDeoptCode 即降级调 host.Arith 慢路径(byte-equal
	// 解释器)。本 PJ2 真接入是 PJ10 luajc 档的字节级核心物理基础。
	//
	// **投机范围**(承 03 §2 IEEE 754 单条 SSE 指令):
	//   - ✅ ADD / SUB / MUL / DIV:单条 SSE binop(F2 0F 58/5C/59/5E C1)
	//   - ❌ MOD:Lua floor-mod 语义(a - floor(a/b)*b)不是单条 SSE,需
	//     fpsub + sse round + sse sub 三指令,留 PJ3+
	//   - ❌ POW:走 math.Pow helper(C runtime),非 SSE 一指令路径
	// 不在白名单的算术族走 host helper 慢路径(与解释器 byte-equal)。
	//
	// **mock host 兜底**:Compile 时 c.hostState.ArenaBaseAddr() 返 0(jit
	// 包内单测 mock 无真 arena)→ 不启用 spec(避免段读 [rbx+0]=读 0 SIGSEGV)。
	// 真 crescent.State 上 ArenaBaseAddr 在 LoadProgram 后非 0,启用 spec。
	useSpec := false
	useSpecRegK := false
	useSpecChain := false
	var specSseOp byte
	var specSseOp2 byte
	var regKValue uint64
	var chainK1Value, chainK2Value uint64
	if archSupportsSpec() && info.chainOp == 0 &&
		c.hostState != nil && c.hostState.ArenaBaseAddr() != 0 {
		if op, ok := archSseOpForArith(info.preludeOp); ok {
			specSseOp = op
			// reg-reg 形态:B/C 都 ≤ 254
			if info.preludeArg <= 254 && info.preludeC <= 254 {
				useSpec = true
			} else if info.preludeArg <= 254 && info.preludeC >= 256 &&
				int(info.preludeC-256) < len(proto.Consts) {
				// reg-K 形态:B 是 reg,C 是常量索引;K 必须是 number(否则
				// 降级 host——投机模板只支持 number 常量,string/bool/table
				// 等需 doArith coercion 逻辑)
				kIdx := int(info.preludeC - 256)
				kVal := proto.Consts[kIdx]
				if value.IsNumber(kVal) {
					useSpecRegK = true
					regKValue = uint64(kVal)
				}
			}
		}
	}
	// **chain reg-K-K**:`R(A) = R(B) op1 K1 op2 K2`(luac 编 `x*2+1` 等)。
	// chainB 在 analyzeArithChainForm 已固定 = retA(中间值衔接),preludeArg
	// 是 op1.B = 原始 reg。
	if archSupportsSpec() && info.chainOp != 0 &&
		c.hostState != nil && c.hostState.ArenaBaseAddr() != 0 {
		op1, ok1 := archSseOpForArith(info.preludeOp)
		op2, ok2 := archSseOpForArith(info.chainOp)
		if ok1 && ok2 && info.preludeArg <= 254 &&
			info.preludeC >= 256 && info.chainC >= 256 &&
			int(info.preludeC-256) < len(proto.Consts) &&
			int(info.chainC-256) < len(proto.Consts) {
			k1Val := proto.Consts[info.preludeC-256]
			k2Val := proto.Consts[info.chainC-256]
			if value.IsNumber(k1Val) && value.IsNumber(k2Val) {
				useSpecChain = true
				specSseOp = op1
				specSseOp2 = op2
				chainK1Value = uint64(k1Val)
				chainK2Value = uint64(k2Val)
			}
		}
	}

	var buf []byte
	if useSpec {
		// 92 字节投机模板。deoptCode 选高位 NaN-box 段且不会被任何合法 Lua
		// 值碰到的特殊值(0xFFFC_DEAD_DEADBE00 = 模仿 deopt 标记)。
		const deoptCode uint64 = 0xFFFCDEAD_DEADBE00
		buf = archEmitArithSpecBinopWithGuard(buf, specSseOp, info.retA,
			uint8(info.preludeArg), uint8(info.preludeC), deoptCode)
		page, err := archMmapCode(buf)
		if err != nil {
			return nil, err
		}
		incSpecRegRegHits() // prove-the-path 白盒命中证据
		return &p4Code{
			proto:         proto,
			codePage:      page,
			jitCtx:        NewJITContext(),
			retA:          info.retA,
			retB:          info.retB,
			retPC:         info.retPC,
			writeRetA:     info.writeRetA,
			preludeOp:     info.preludeOp,
			preludeArg:    info.preludeArg,
			preludeC:      info.preludeC,
			cmpA:          info.cmpA,
			chainOp:       info.chainOp,
			chainB:        info.chainB,
			chainC:        info.chainC,
			host:          c.hostState,
			useSpec:       true,
			specDeoptCode: deoptCode,
		}, nil
	}
	if useSpecRegK {
		// 73 字节 reg-K 投机模板:单 guard B(reg)+ 烧 K imm64 直发段 +
		// SSE binop + 写回 + deopt block。
		const deoptCode uint64 = 0xFFFCDEAD_DEADBE00
		buf = archEmitArithSpecBinopRegKWithGuard(buf, specSseOp, info.retA,
			uint8(info.preludeArg), regKValue, deoptCode)
		page, err := archMmapCode(buf)
		if err != nil {
			return nil, err
		}
		incSpecRegKHits() // prove-the-path 白盒命中证据
		return &p4Code{
			proto:         proto,
			codePage:      page,
			jitCtx:        NewJITContext(),
			retA:          info.retA,
			retB:          info.retB,
			retPC:         info.retPC,
			writeRetA:     info.writeRetA,
			preludeOp:     info.preludeOp,
			preludeArg:    info.preludeArg,
			preludeC:      info.preludeC,
			cmpA:          info.cmpA,
			chainOp:       info.chainOp,
			chainB:        info.chainB,
			chainC:        info.chainC,
			host:          c.hostState,
			useSpec:       true,
			specDeoptCode: deoptCode,
		}, nil
	}
	if useSpecChain {
		// 92 字节 chain 模板:单 guard reg-B + 烧 K1/K2 imm64 + 两次 SSE binop
		// 经 xmm0 链式衔接 + 写回 + deopt block。一次 mmap 段调用完成两次算术,
		// 省一次 boundary + reg-stack 中转。
		//
		// **chainOp 保留**:Run 路径 deopt 时需要调 host.Arith 两次串行
		// (op1 + op2)以 byte-equal 解释器。compiler 不能 clear chainOp,
		// 否则 deopt fallback 只跑 op1 = 错果(chain 模板执行成功路径不读
		// chainOp;deopt 路径读 chainOp 做双慢调)。writeRetA=false 因 mmap
		// 段已 movsd [rbx+A*8] xmm0 写好 R(A)。
		const deoptCode uint64 = 0xFFFCDEAD_DEADBE00
		buf = archEmitArithSpecChainKKWithGuard(buf, specSseOp, specSseOp2,
			info.retA, uint8(info.preludeArg),
			chainK1Value, chainK2Value, deoptCode)
		page, err := archMmapCode(buf)
		if err != nil {
			return nil, err
		}
		incSpecChainHits() // prove-the-path 白盒命中证据
		return &p4Code{
			proto:         proto,
			codePage:      page,
			jitCtx:        NewJITContext(),
			retA:          info.retA,
			retB:          info.retB,
			retPC:         info.retPC,
			writeRetA:     info.writeRetA,
			preludeOp:     info.preludeOp,
			preludeArg:    info.preludeArg,
			preludeC:      info.preludeC,
			cmpA:          info.cmpA,
			chainOp:       info.chainOp, // 保留:Run 端 deopt 时调 host.Arith × 2
			chainB:        info.chainB,
			chainC:        info.chainC,
			host:          c.hostState,
			useSpec:       true,
			specDeoptCode: deoptCode,
		}, nil
	}

	// 发射:LOADK/RETURN 模板(arch 路由——amd64 mov rax,imm + ret 11 字节;
	// arm64 movz+movk×3 + ret 20 字节)。writeRetA=false 时 value 不被使用
	// (mmap 段返回值是 dummy),仍发模板因为 mmap 段必须非空。
	buf = archEmitLoadKReturn(buf, info.value)

	page, err := archMmapCode(buf)
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
		cmpA:       info.cmpA,
		chainOp:    info.chainOp,
		chainB:     info.chainB,
		chainC:     info.chainC,
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
//   - 长度 2/3:GETGLOBAL A Bx + RETURN A 2(prelude 路径调 host.DoGetGlobal,
//     可 ERR 冒泡)
//   - 长度 2/3:SETTABLE A B C + RETURN A 1(setter 0 返回值,prelude 路径
//     调 host.SetTable,经 IC + __newindex 元方法链,可 ERR 冒泡)
//   - 长度 2/3:SETGLOBAL A Bx + RETURN A 1(setter,prelude 路径调
//     host.DoSetGlobal,可 ERR 冒泡)
var ErrCompileUnsupportedShape = errors.New("internal/gibbous/jit: P4 PJ7 unsupported shape (expected: single RETURN A B / single-BB MOVE|GETUPVAL|LOADK|LOADBOOL|LOADNIL|ADD..POW|UNM|LEN|NEWTABLE|GETTABLE|GETGLOBAL|SETTABLE|SETGLOBAL + RETURN A 2 (getter) / 1 (setter))")
