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
	//   - forLimitReg + forLimitIsReg:reg-limit 形态用 R(limitReg) 而非 K
	//   - forLimitUpvalIdx:upvalue-limit 形态时的 upvalue 索引 + 1(1-based;
	//     0 表示不走 upvalue 路径,直接走 MOVE/LOADK 形态)。Run 端先调
	//     host.GetUpval(idx-1) 写 R(forLimitReg) 槽,然后 callJITSpec 走
	//     reg-limit 模板字节级 inline。
	//   - hasBody + bodyOp/bodyKValue/forBodyAS:body 含单 reg-K op 形态
	//     (`s = s op K`):hasBody=true 时模板含 init R(aS)=K_s + body
	//     inline。
	isForLoop        bool
	forA             uint8
	forInitK         uint64
	forLimitK        uint64
	forStepK         uint64
	forLimitReg      uint8 // limit 是 reg 时的源寄存器号(forLimitIsReg=true)
	forLimitIsReg    bool  // true = limit 从 R(forLimitReg) 读 + IsNumber guard;false = K 编译期烧 imm
	forLimitUpvalIdx uint8 // upvalue-limit 形态的 upvalue 索引 + 1(0 = 不走 upval 路径)
	hasBody          bool  // true = FORLOOP 含 reg-K body op
	bodyOp           uint8 // body 的 SSE op 字节(SseOpAddsd / Subsd / Mulsd / Divsd)
	bodyKValue       uint64
	forBodyAS        uint8  // body 的 R(aS) 寄存器号(s 槽)
	forBodyKS        uint64 // body 形态下 R(aS) 的初始 K 值(K_s)
	// 二段 body 形态(2 个 reg-K op,共享 R(aS),body2 模板复用 xmm3
	// 跨两段省一次 load/store):
	hasBody2    bool   // true = 二段 body 形态(`s=s op1 K1; s=s op2 K2`)
	bodyOp2     uint8  // 第二段 op SSE 字节
	bodyKValue2 uint64 // 第二段 K 值

	// PJ4 表 IC ArrayHit 形态(`function(t) return t[K] end`):
	//   - icArrayHit = true:走 PJ4 IC 直达槽 inline 模板
	//   - icAReg / icBReg:GETTABLE A B
	//   - icStableShape / icStableIndex:编译期从 feedback / IC slot 固化
	icArrayHit    bool
	icAReg        uint8
	icBReg        uint8
	icStableShape uint32
	icStableIndex uint32

	// PJ4 表 IC NodeHit 形态(对位 ArrayHit 但 IC kind=NodeHit,hash 段):
	//   - icNodeHit = true:走 PJ4 IC NodeHit 字节级直达槽 inline 模板
	//   - icStableKey:编译期从 proto.Consts[KIdx] 固化 stableKey NaN-box,
	//     模板内验 NodeKey == stableKey 防键退化
	icNodeHit   bool
	icStableKey uint64

	// PJ4 表 IC SETTABLE ArrayHit 形态(`function(t,v) t[K] = v end`):
	//   - icSetArrayHit = true:走 PJ4 SETTABLE IC 字节级 inline 反向写模板
	//   - icSetCReg:value 寄存器号 R(C)(C<256,reg 而非常量)
	icSetArrayHit bool
	icSetCReg     uint8

	// PJ4 SELF IC ArrayHit 形态(`function(obj) obj:method() end` 前段 SELF):
	//   - icSelfArrayHit = true:走 PJ4 SELF IC 字节级 inline 模板(139 字节)
	//   - 复用 icAReg(SELF.A,method 结果)/ icBReg(SELF.B,obj)/
	//     icStableShape / icStableIndex 字段。R(A+1) 由模板从 R(B) 拷写。
	icSelfArrayHit bool

	// PJ4 SETTABLE NodeHit 形态(`function(t, v) t["x"] = v end`):
	//   - icSetNodeHit = true:走 PJ4 SETTABLE IC NodeHit 字节级 inline 模板
	//     (140 字节,hash 段 NodeKey 比对 + 反向 store NodeVal)
	//   - 复用 icSetCReg(value reg)/ icStableShape / icStableIndex / icStableKey
	icSetNodeHit bool

	// PJ4 SELF NodeHit 形态(`function(obj) obj:method() end` 真常见 OOP 调用):
	//   - icSelfNodeHit = true:走 PJ4 SELF IC NodeHit 字节级 inline 模板
	//     (166 字节,SELF ArrayHit 139 + key 比对 27)
	//   - 复用 icAReg(SELF.A 即 method 结果)/ icBReg(SELF.B 即 obj)/
	//     icStableShape / icStableIndex / icStableKey 字段
	icSelfNodeHit bool

	// PJ5 CALL void 形态(`function(g) g() end` 类):
	//   - isCallVoid = true:Run 端 prelude 路径调 host.CallBaseline 完成 baseline
	//     CALL(byte-equal P1 doCall 分派,host/crescent/__call/gibbous 全覆盖)
	//   - isCallUpval = true:形态 B 即 GETUPVAL+CALL+RETURN void(被调来源是
	//     upvalue,如外层 local fn);false 即形态 A MOVE+CALL+RETURN void
	//     (被调来源是 parameter / local reg)
	//   - callA / callB / callC:CALL A B C 三字段直传给 host.CallBaseline
	//
	// **retA / retPC 字段设定**(setter 形态 0 返值,与既有 setter 路径
	// SETTABLE/SETGLOBAL 同款):retA=0(Run 路径不读 retA,因 setter 形态
	// host.DoReturn 不写 R(A));retPC=2(RETURN 在 pc 2,CALL 自身 pc=1
	// 由 Run 端 retPC-1 现算 callPC,prelude switch CALL case 内推导)。
	//
	// preludeArg:形态 A 时 = MOVE.B(源 reg)/ 形态 B 时 = GETUPVAL.B
	// (upvalue 索引)
	//
	// 形态识别在 analyzeCallVoidForm,典型 luac 编译形态(长度 3、4、5):
	//   形态 A0/B0:0 参 0 返(长度 3)
	//   形态 A1K/B1K:1 K 参 0 返(长度 4)
	//   形态 A1R/B1R:1 reg 参 0 返(长度 4)
	//   形态 AR1/BR1:0 参 1 返 getter(长度 4 含 dead RETURN)
	//   形态 A2K/B2K:2 K 参 0 返(长度 5,本批扩展)
	isCallVoid     bool
	isCallUpval    bool
	callA          uint8
	callB          uint8
	callC          uint8
	callArgCount   uint8 // 0 / 1 / 2 / 3
	callMultiRet   uint8 // N=0/1 既有形态(setter/getter 1 返);N>=2 表 N 返值 getter
	callArg1IsK    bool
	callArg1K      uint64
	callArg1RegSrc uint8
	callArg2IsK    bool
	callArg2K      uint64
	callArg2RegSrc uint8
	callArg3IsK    bool
	callArg3K      uint64
	callArg3RegSrc uint8
	callArg4IsK    bool
	callArg4K      uint64
	callArg4RegSrc uint8
	callArg5IsK    bool
	callArg5K      uint64
	callArg5RegSrc uint8
	callArg6IsK    bool
	callArg6K      uint64
	callArg6RegSrc uint8
	callArg7IsK    bool
	callArg7K      uint64
	callArg7RegSrc uint8

	// PJ5 TAILCALL 形态(`function() return f() end` 类):
	//   - isTailCall = true:Run 端 prelude 路径调 host.TailCall 三态分支
	//     (0=Lua 尾完成跳过 DoReturn / 1=ERR / 2=host 落尾随 dead RETURN B=0 to-top)
	//
	// luac stmtReturn(frontend/compile/stmt.go::stmtReturn)对单 CallExpr 返回
	// 翻 TAILCALL + RETURN A B=0(dead 尾随)+ 隐式 RETURN A=0 B=1。形态在
	// CALL void 同字段集复用(callA/callB/callC/callArgCount/callArg1*/callArg2K +
	// isCallUpval + preludeArg)但 retPC 指向 dead RETURN B=0 to-top 而非 setter
	// RETURN B=1,本帧由 host.TailCall 完成或 dead RETURN to-top 转发。
	//
	// 与 isCallVoid 互斥(preludeOp=CALL → isCallVoid;preludeOp=TAILCALL →
	// isTailCall)。形态识别在 analyzeTailCallForm。
	isTailCall bool

	// PJ5 SELF method call inline 形态(`obj:method(args)` 类):
	//   - isSelfCall = true:Run 端 prelude 路径调 host.Self + (CallBaseline|TailCall)
	//     完成 SELF + CALL/TAILCALL byte-equal P1 doCall 分派。
	//   - selfCallA / selfMethodRK:SELF.A(method 结果)/ SELF.C(RK 方法名常量索引)
	//   - selfRecvSrcReg / selfRecvIsUpval:receiver 来自 R(selfRecvSrcReg) 还是 upvalue
	//     索引(luac 编 SELF 前 MOVE/GETUPVAL 入 R(SELF.A)=R(SELF.B) recv)
	//
	// 复用 isTailCall / callA / callB / callC / callArgCount / callArg1*..callArg7*
	// 字段 — SELF + CALL = isCallVoid=false isTailCall=false isSelfCall=true CALL 分支;
	// SELF + TAILCALL = isTailCall=true isSelfCall=true TAILCALL 分支。
	//
	// 形态识别在 analyzeSelfCallForm。
	isSelfCall      bool
	selfCallA       uint8  // SELF.A = method 结果寄存器(同 callA)
	selfMethodRK    uint16 // SELF.C 字段(RK 方法名常量索引 0-511)
	selfRecvSrcReg  uint8  // recv 来源 reg(form M*)/ upvalue 索引(form U*)
	selfRecvIsUpval bool   // true = recv 来自 upvalue;false = 来自 reg

	// PJ5 SELF + CALL spec template 接入(承 §9.10 PJ4 EmitSelfNodeHit 复用):
	//   - useSpecSelfCall = true:SELF 段走字节级 EmitSelfNodeHit 模板(IC NodeHit
	//     guard + stableKey 比对 + NodeVal store R(A)=method),跳过 host.Self
	//     round-trip;失败 deopt 降级 host.Self。CALL 段仍走 host.CallBaseline。
	//   - 复用 icAReg/icBReg/icStableShape/icStableIndex/icStableKey(PJ4 SELF
	//     NodeHit 同字段集)。
	useSpecSelfCall bool
}

// analyzeGetTableArrayHit 识别 PJ4 IC ArrayHit 形态:
// `function(t) return t[K] end`(GETTABLE A B C(常量 K idx)+ RETURN A 2)。
//
// 与 analyzeShape 的 GETTABLE 路径**互补**:analyzeShape 走 host.GetTable
// 慢路径;本函数走字节级 IC ArrayHit 直达槽 inline,跳过哈希。
//
// **触发条件**(全部满足才返 true):
//   - Code 长度 2 或 3([0]=GETTABLE / [1]=RETURN / [2]?=dead RETURN)
//   - GETTABLE A B C:A==RETURN.A,B<=254,C>=256(K 常量索引)
//   - RETURN A=GETTABLE.A B=2
//   - proto.IC[0].Kind == ICKindArrayHit(P1 解释器观测过 array 命中)
//   - feedback.Points[0].Kind == FBTableMono(P2 聚合后稳定 mono)
//   - feedback.Points[0].Confidence >= 0.99(投机阈值)
//   - feedback / proto.IC stableShape & stableIndex 一致(无 race 时一致)
//
// 失败任一条件返 (shapeInfo{}, false)— 走原 analyzeShape 路径(host helper)。
func analyzeGetTableArrayHit(proto *bytecode.Proto, feedback *bridge.TypeFeedback) (shapeInfo, bool) {
	codeLen := len(proto.Code)
	if codeLen != 2 && codeLen != 3 {
		return shapeInfo{}, false
	}
	if bytecode.Op(proto.Code[0]) != bytecode.GETTABLE ||
		bytecode.Op(proto.Code[1]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	if codeLen == 3 && bytecode.Op(proto.Code[2]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	gtA := bytecode.A(proto.Code[0])
	gtB := bytecode.B(proto.Code[0])
	gtC := bytecode.C(proto.Code[0])
	retA := bytecode.A(proto.Code[1])
	retB := bytecode.B(proto.Code[1])
	if gtA != retA || retB != 2 {
		return shapeInfo{}, false
	}
	if gtB > 254 || gtC < 256 {
		// C 必须是常量索引(>=256)— 否则 key 是动态 reg,IC slot 可能
		// 轮换不同 key,字节级 inline 不能假设 stableIndex
		return shapeInfo{}, false
	}
	// IC slot 检查(proto.IC 长度 = len(proto.Code))
	if len(proto.IC) <= 0 {
		return shapeInfo{}, false
	}
	icSlot := proto.IC[0]
	if icSlot.Kind != bytecode.ICKindArrayHit {
		return shapeInfo{}, false
	}
	// feedback 检查(可能 nil — wireP4 传入时 mainPath 经 ProfileData,
	// jit 包内单测可能传 nil)
	if feedback == nil || len(feedback.Points) < 1 {
		return shapeInfo{}, false
	}
	pf := feedback.Points[0]
	if pf.Kind != bridge.FBTableMono || pf.Confidence < 0.99 {
		return shapeInfo{}, false
	}
	// stableShape / stableIndex 必须一致(feedback 与 IC slot 同源,
	// 但 race 时可能略有偏差;严苛要求一致才投机)
	if pf.StableShape != icSlot.Shape || pf.StableIndex != icSlot.Index {
		return shapeInfo{}, false
	}

	return shapeInfo{
		ok:            true,
		retA:          uint8(retA),
		retB:          uint8(retB),
		retPC:         1,
		preludeOp:     uint8(bytecode.GETTABLE), // Run 端 deopt 走 host.GetTable
		preludeArg:    uint32(gtB),
		preludeC:      uint16(gtC),
		icArrayHit:    true,
		icAReg:        uint8(retA),
		icBReg:        uint8(gtB),
		icStableShape: pf.StableShape,
		icStableIndex: pf.StableIndex,
	}, true
}

// analyzeGetTableNodeHit 识别 PJ4 IC NodeHit 形态:
// `function(t) return t["x"] end`(GETTABLE A B C(常量 K idx)+ RETURN A 2),
// 其中 IC[0].Kind=NodeHit(P1 解释器命中 hash 段而非 array 段)。
//
// 与 analyzeGetTableArrayHit 几乎同款触发条件,差异:
//   - proto.IC[0].Kind == ICKindNodeHit(P1 解释器观测过 hash 命中)
//   - 编译期固化 stableKey = proto.Consts[KIdx]:NodeHit 模板需要验
//     NodeKey == stableKey,防键退化(__index 链 / rehash 等场景)
//
// **stableKey 编译期固化条件**:
//   - proto.Consts 索引有效(KIdx < len(Consts))
//   - 该 Const 不是 Nil(LoadProgram 已为字符串常量 intern,数字常量
//     编译期就装好;Nil 槽是异常 — 不投机)
//
// 失败任一条件返 (shapeInfo{}, false)—— 走 analyzeShape host.GetTable 路径
// (byte-equal P1)。
func analyzeGetTableNodeHit(proto *bytecode.Proto, feedback *bridge.TypeFeedback) (shapeInfo, bool) {
	codeLen := len(proto.Code)
	if codeLen != 2 && codeLen != 3 {
		return shapeInfo{}, false
	}
	if bytecode.Op(proto.Code[0]) != bytecode.GETTABLE ||
		bytecode.Op(proto.Code[1]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	if codeLen == 3 && bytecode.Op(proto.Code[2]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	gtA := bytecode.A(proto.Code[0])
	gtB := bytecode.B(proto.Code[0])
	gtC := bytecode.C(proto.Code[0])
	retA := bytecode.A(proto.Code[1])
	retB := bytecode.B(proto.Code[1])
	if gtA != retA || retB != 2 {
		return shapeInfo{}, false
	}
	if gtB > 254 || gtC < 256 {
		// C 必须是常量索引(>=256)— 否则 key 是动态 reg,IC slot 可能
		// 轮换不同 key
		return shapeInfo{}, false
	}
	// IC slot 检查
	if len(proto.IC) <= 0 {
		return shapeInfo{}, false
	}
	icSlot := proto.IC[0]
	if icSlot.Kind != bytecode.ICKindNodeHit {
		return shapeInfo{}, false
	}
	// feedback 检查
	if feedback == nil || len(feedback.Points) < 1 {
		return shapeInfo{}, false
	}
	pf := feedback.Points[0]
	if pf.Kind != bridge.FBTableMono || pf.Confidence < 0.99 {
		return shapeInfo{}, false
	}
	if pf.StableShape != icSlot.Shape || pf.StableIndex != icSlot.Index {
		return shapeInfo{}, false
	}

	// **stableKey 编译期固化**(NodeHit 比 ArrayHit 多这一步):
	// 从 proto.Consts[KIdx] 取 NaN-box 键(LoadProgram 已 intern 字符串)。
	kIdx := bytecode.KIdx(int(gtC))
	if kIdx < 0 || kIdx >= len(proto.Consts) {
		return shapeInfo{}, false
	}
	stableKey := uint64(proto.Consts[kIdx])
	// **Nil 槽校验**:`value.Nil = 0xFFF8_0000_0000_0000`(TagNil=0xFFF8,
	// 承 internal/value/value.go::Nil)。LoadProgram 未装载完成的字符串槽
	// 是真 Nil(非 0)。注意:**不能用 stableKey == 0 当 sentinel**——
	// IEEE 754 数字键 0.0 NaN-box 是 0x0000_0000_0000_0000,与 sentinel 撞
	// 型,数字键 `t[0]` 会被误拒投机(本仓承外部审查反馈 commit c7034b2
	// 修复)。
	if stableKey == uint64(value.Nil) {
		return shapeInfo{}, false
	}

	return shapeInfo{
		ok:            true,
		retA:          uint8(retA),
		retB:          uint8(retB),
		retPC:         1,
		preludeOp:     uint8(bytecode.GETTABLE), // Run 端 deopt 走 host.GetTable
		preludeArg:    uint32(gtB),
		preludeC:      uint16(gtC),
		icNodeHit:     true,
		icAReg:        uint8(retA),
		icBReg:        uint8(gtB),
		icStableShape: pf.StableShape,
		icStableIndex: pf.StableIndex,
		icStableKey:   stableKey,
	}, true
}

// analyzeSetTableArrayHit 识别 PJ4 SETTABLE IC ArrayHit 形态:
// `function(t,v) t[K] = v end` 中 K 是 array 段命中的数字常量,v 是 reg。
//
// **形态**(luac 编 2 op,setter 形态):
//   - [0] SETTABLE A B C:A=R(t) 表 reg,B=K idx(>=256)key 常量,C=R(v) value reg(<256)
//   - [1] RETURN A 1(setter 0 返回值)
//
// **触发条件**(全部满足才返 true):
//   - Code 长度 2 或 3
//   - SETTABLE A B C:A<=254,B>=256(K 常量索引),C<256(value 是 reg)
//   - RETURN B=1(setter)
//   - proto.IC[0].Kind == ICKindArrayHit(P1 解释器观测过 array 命中)
//   - feedback.Points[0].Kind == FBTableMono + Confidence >= 0.99
//   - stableShape / stableIndex 一致
//
// **设计简化**(承 pj4_template.go::EmitSetTableArrayHit godoc):
//   - 不验现有 array[stableIndex] != nil(防新键路径)— 依赖 P1 解释器
//     在键退化场景 bump gen + RequestRefresh,本帧已写错的接受
//   - 不验 __newindex 元表存在(meta freeze 假设)— 元方法场景应触发
//     gen change 由 IC 失效路径处理
//
// 这两条简化是 PJ4 SETTABLE 工程边界,严密版留 PJ4+。
//
// 失败任一条件返 (shapeInfo{}, false)—— 走 analyzeShape host.SetTable
// (byte-equal P1)。
func analyzeSetTableArrayHit(proto *bytecode.Proto, feedback *bridge.TypeFeedback) (shapeInfo, bool) {
	codeLen := len(proto.Code)
	if codeLen != 2 && codeLen != 3 {
		return shapeInfo{}, false
	}
	if bytecode.Op(proto.Code[0]) != bytecode.SETTABLE ||
		bytecode.Op(proto.Code[1]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	if codeLen == 3 && bytecode.Op(proto.Code[2]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	stA := bytecode.A(proto.Code[0])
	stB := bytecode.B(proto.Code[0])
	stC := bytecode.C(proto.Code[0])
	retB := bytecode.B(proto.Code[1])
	if retB != 1 { // setter 必须 0 返回值
		return shapeInfo{}, false
	}
	if stA > 254 || stB < 256 || stC > 254 {
		// A: 表 reg <=254
		// B: K 常量索引 >=256(动态 reg key 会让 stableIndex 不稳)
		// C: value reg <256(常量 value 不投机 — 烧 imm 到 rdx 需另一原语)
		return shapeInfo{}, false
	}
	// IC slot 检查
	if len(proto.IC) <= 0 {
		return shapeInfo{}, false
	}
	icSlot := proto.IC[0]
	if icSlot.Kind != bytecode.ICKindArrayHit {
		return shapeInfo{}, false
	}
	// feedback 检查
	if feedback == nil || len(feedback.Points) < 1 {
		return shapeInfo{}, false
	}
	pf := feedback.Points[0]
	if pf.Kind != bridge.FBTableMono || pf.Confidence < 0.99 {
		return shapeInfo{}, false
	}
	if pf.StableShape != icSlot.Shape || pf.StableIndex != icSlot.Index {
		return shapeInfo{}, false
	}

	return shapeInfo{
		ok:            true,
		retA:          uint8(stA),
		retB:          uint8(retB),
		retPC:         1,
		preludeOp:     uint8(bytecode.SETTABLE), // Run 端 deopt 走 host.SetTable
		preludeArg:    uint32(stB),
		preludeC:      uint16(stC),
		icSetArrayHit: true,
		icAReg:        uint8(stA),
		icSetCReg:     uint8(stC),
		icStableShape: pf.StableShape,
		icStableIndex: pf.StableIndex,
	}, true
}

// analyzeSelfArrayHit 识别 PJ4 SELF IC ArrayHit 形态:
// `function(obj) return obj:method() end` 前段 SELF + RETURN 形态。
//
// **形态识别难点**:SELF 后必接 CALL 才完整,RETURN 直接接 SELF 不是
// luac 真实编译路径(`return obj:method()` 编 SELF + CALL + RETURN R(A) B)。
// **但**:`local m = obj:method` 编 SELF + RETURN(R(A) 是 method 函数,
// R(A+1) 是 obj 但被忽略)— **这才是 SELF + RETURN 形态的真实代码源**,
// 罕见但可能。本批保守接入 SELF + RETURN 2 op 形态(SELF 写 R(A),
// RETURN A 2 取 R(A) 返回)。
//
// **形态**(luac 编 2 op):
//   - [0] SELF A B C:A=method 结果 reg,B=obj reg,C=method key RK(必 >=256 常量索引)
//   - [1] RETURN A 2(取 R(A) method 函数,返回单值)
//
// **触发条件**:
//   - Code 长度 2 或 3
//   - SELF A B C:A<=253(留 R(A+1) 槽),B<=254,C>=256(K 常量)
//   - RETURN A=SELF.A B=2
//   - proto.IC[0].Kind=ArrayHit + feedback FBTableMono + shape/index 一致
//
// 失败任一条件返 (shapeInfo{}, false)—— 走 analyzeShape 路径(若有
// SELF + RETURN 同款 host helper 支持)或 ErrCompileUnsupportedShape。
func analyzeSelfArrayHit(proto *bytecode.Proto, feedback *bridge.TypeFeedback) (shapeInfo, bool) {
	codeLen := len(proto.Code)
	if codeLen != 2 && codeLen != 3 {
		return shapeInfo{}, false
	}
	if bytecode.Op(proto.Code[0]) != bytecode.SELF ||
		bytecode.Op(proto.Code[1]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	if codeLen == 3 && bytecode.Op(proto.Code[2]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	selfA := bytecode.A(proto.Code[0])
	selfB := bytecode.B(proto.Code[0])
	selfC := bytecode.C(proto.Code[0])
	retA := bytecode.A(proto.Code[1])
	retB := bytecode.B(proto.Code[1])
	if selfA != retA || retB != 2 {
		return shapeInfo{}, false
	}
	// A 最大 253(留 R(A+1) 槽 ≤ 254);B <=254;C>=256(K 常量)
	if selfA > 253 || selfB > 254 || selfC < 256 {
		return shapeInfo{}, false
	}
	// IC slot 检查
	if len(proto.IC) <= 0 {
		return shapeInfo{}, false
	}
	icSlot := proto.IC[0]
	if icSlot.Kind != bytecode.ICKindArrayHit {
		return shapeInfo{}, false
	}
	// feedback 检查
	if feedback == nil || len(feedback.Points) < 1 {
		return shapeInfo{}, false
	}
	pf := feedback.Points[0]
	if pf.Kind != bridge.FBSelfMono || pf.Confidence < 0.99 {
		return shapeInfo{}, false
	}
	if pf.StableShape != icSlot.Shape || pf.StableIndex != icSlot.Index {
		return shapeInfo{}, false
	}

	return shapeInfo{
		ok:             true,
		retA:           uint8(retA),
		retB:           uint8(retB),
		retPC:          1,
		preludeOp:      uint8(bytecode.SELF), // Run 端 deopt 走 host.SelfTable(同 GetTable 路径)
		preludeArg:     uint32(selfB),
		preludeC:       uint16(selfC),
		icSelfArrayHit: true,
		icAReg:         uint8(retA),
		icBReg:         uint8(selfB),
		icStableShape:  pf.StableShape,
		icStableIndex:  pf.StableIndex,
	}, true
}

// analyzeSetTableNodeHit 识别 PJ4 SETTABLE IC NodeHit 形态:
// `function(t, v) t["x"] = v end` 中键是字符串/任意 K 命中 hash 段。
//
// **形态**(luac 编 2 op,setter):
//   - [0] SETTABLE A B C:A=R(t),B=K idx(>=256)key 常量,C=R(v) value reg(<256)
//   - [1] RETURN A 1(setter 0 返回值)
//
// **触发条件**:
//   - Code 长度 2 或 3
//   - SETTABLE A B C:A<=254,B>=256(K 常量),C<256(value 是 reg)
//   - RETURN B=1
//   - proto.IC[0].Kind == ICKindNodeHit
//   - feedback FBTableMono + Confidence>=0.99 + shape/index 一致
//   - stableKey 从 proto.Consts[KIdx] 编译期固化(防 Nil 槽:value.Nil)
//
// 失败任一条件返 (shapeInfo{}, false)—— 走 analyzeShape host.SetTable
// byte-equal P1(经 icSetTable + __newindex 元方法链)。
func analyzeSetTableNodeHit(proto *bytecode.Proto, feedback *bridge.TypeFeedback) (shapeInfo, bool) {
	codeLen := len(proto.Code)
	if codeLen != 2 && codeLen != 3 {
		return shapeInfo{}, false
	}
	if bytecode.Op(proto.Code[0]) != bytecode.SETTABLE ||
		bytecode.Op(proto.Code[1]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	if codeLen == 3 && bytecode.Op(proto.Code[2]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	stA := bytecode.A(proto.Code[0])
	stB := bytecode.B(proto.Code[0])
	stC := bytecode.C(proto.Code[0])
	retB := bytecode.B(proto.Code[1])
	if retB != 1 {
		return shapeInfo{}, false
	}
	if stA > 254 || stB < 256 || stC > 254 {
		return shapeInfo{}, false
	}
	if len(proto.IC) <= 0 {
		return shapeInfo{}, false
	}
	icSlot := proto.IC[0]
	if icSlot.Kind != bytecode.ICKindNodeHit {
		return shapeInfo{}, false
	}
	if feedback == nil || len(feedback.Points) < 1 {
		return shapeInfo{}, false
	}
	pf := feedback.Points[0]
	if pf.Kind != bridge.FBTableMono || pf.Confidence < 0.99 {
		return shapeInfo{}, false
	}
	if pf.StableShape != icSlot.Shape || pf.StableIndex != icSlot.Index {
		return shapeInfo{}, false
	}
	// stableKey 编译期固化(同 GetTable NodeHit)
	kIdx := bytecode.KIdx(int(stB))
	if kIdx < 0 || kIdx >= len(proto.Consts) {
		return shapeInfo{}, false
	}
	stableKey := uint64(proto.Consts[kIdx])
	if stableKey == uint64(value.Nil) {
		// LoadProgram 未装载字符串槽(罕见但防御)
		return shapeInfo{}, false
	}

	return shapeInfo{
		ok:            true,
		retA:          uint8(stA),
		retB:          uint8(retB),
		retPC:         1,
		preludeOp:     uint8(bytecode.SETTABLE),
		preludeArg:    uint32(stB),
		preludeC:      uint16(stC),
		icSetNodeHit:  true,
		icAReg:        uint8(stA),
		icSetCReg:     uint8(stC),
		icStableShape: pf.StableShape,
		icStableIndex: pf.StableIndex,
		icStableKey:   stableKey,
	}, true
}

// analyzeSelfNodeHit 识别 PJ4 SELF IC NodeHit 形态:
// `local m = obj:method` / `obj:method()` 单 BB 形态——method 是字符串
// ident → hash 段命中。这是 real-world `obj:method()` 调用的典型形态
// (luac 编 SELF A=R(m) B=R(obj) C=K(string),IC[0]=NodeHit)。
//
// **形态**(luac 编 2 op):
//   - [0] SELF A B C:A<=253(留 R(A+1) 槽<=254),B<=254,C>=256(K 常量 string)
//   - [1] RETURN A 2(取 R(A) method 函数)
//
// **触发条件**:
//   - Code 长度 2 或 3
//   - SELF A B C + RETURN A 2 形态守卫
//   - proto.IC[0].Kind == ICKindNodeHit
//   - feedback FBTableMono + Confidence >= 0.99 + shape/index 一致
//   - stableKey 编译期固化(LoadProgram 已 intern 字符串)
//
// 失败任一条件返 (shapeInfo{}, false)—— 走 analyzeShape 路径(若 SELF +
// RETURN 同款 host helper 支持)或 ErrCompileUnsupportedShape。
func analyzeSelfNodeHit(proto *bytecode.Proto, feedback *bridge.TypeFeedback) (shapeInfo, bool) {
	codeLen := len(proto.Code)
	if codeLen != 2 && codeLen != 3 {
		return shapeInfo{}, false
	}
	if bytecode.Op(proto.Code[0]) != bytecode.SELF ||
		bytecode.Op(proto.Code[1]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	if codeLen == 3 && bytecode.Op(proto.Code[2]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	selfA := bytecode.A(proto.Code[0])
	selfB := bytecode.B(proto.Code[0])
	selfC := bytecode.C(proto.Code[0])
	retA := bytecode.A(proto.Code[1])
	retB := bytecode.B(proto.Code[1])
	if selfA != retA || retB != 2 {
		return shapeInfo{}, false
	}
	if selfA > 253 || selfB > 254 || selfC < 256 {
		return shapeInfo{}, false
	}
	if len(proto.IC) <= 0 {
		return shapeInfo{}, false
	}
	icSlot := proto.IC[0]
	if icSlot.Kind != bytecode.ICKindNodeHit {
		return shapeInfo{}, false
	}
	if feedback == nil || len(feedback.Points) < 1 {
		return shapeInfo{}, false
	}
	pf := feedback.Points[0]
	if pf.Kind != bridge.FBSelfMono || pf.Confidence < 0.99 {
		return shapeInfo{}, false
	}
	if pf.StableShape != icSlot.Shape || pf.StableIndex != icSlot.Index {
		return shapeInfo{}, false
	}
	// stableKey 编译期固化
	kIdx := bytecode.KIdx(int(selfC))
	if kIdx < 0 || kIdx >= len(proto.Consts) {
		return shapeInfo{}, false
	}
	stableKey := uint64(proto.Consts[kIdx])
	if stableKey == uint64(value.Nil) {
		return shapeInfo{}, false
	}

	return shapeInfo{
		ok:            true,
		retA:          uint8(retA),
		retB:          uint8(retB),
		retPC:         1,
		preludeOp:     uint8(bytecode.SELF), // Run 端 deopt 走 host.GetTable(P1 SELF case 同源)
		preludeArg:    uint32(selfB),
		preludeC:      uint16(selfC),
		icSelfNodeHit: true,
		icAReg:        uint8(retA),
		icBReg:        uint8(selfB),
		icStableShape: pf.StableShape,
		icStableIndex: pf.StableIndex,
		icStableKey:   stableKey,
	}, true
}

// analyzeForLoopBody2Form 识别二段 body 形态:`local s=K_s; for i=K1,K2 do
// s = s op1 K3; s = s op2 K4 end; return s`。luac 编 10/11 op,体内含两个
// 串行 reg-K arith 写到同一 R(aS)。
//
// luac 编码(以 `local s=0; for i=1,5 do s=s+1; s=s*2 end; return s` 为例):
//
//	[0] LOADK    A_s     -K_s  ; s=0
//	[1..3] LOADK A_init/+1/+2  ; init/limit/step
//	[4] FORPREP  A_init  sBx=2 ; jmp 到 body[6]
//	[5] arith1   A_s A_s C(K_body1)
//	[6] arith2   A_s A_s C(K_body2)
//	[7] FORLOOP  A_init  sBx=-3 ; jmp 回 [5]
//	[8] RETURN   A_s     B=2
//	[9] dead RETURN(可选)
func analyzeForLoopBody2Form(proto *bytecode.Proto) (shapeInfo, bool) {
	codeLen := len(proto.Code)
	if codeLen != 9 && codeLen != 10 {
		return shapeInfo{}, false
	}
	if bytecode.Op(proto.Code[0]) != bytecode.LOADK ||
		bytecode.Op(proto.Code[1]) != bytecode.LOADK ||
		bytecode.Op(proto.Code[2]) != bytecode.LOADK ||
		bytecode.Op(proto.Code[3]) != bytecode.LOADK ||
		bytecode.Op(proto.Code[4]) != bytecode.FORPREP ||
		bytecode.Op(proto.Code[7]) != bytecode.FORLOOP ||
		bytecode.Op(proto.Code[8]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	if codeLen == 10 && bytecode.Op(proto.Code[9]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	bodyOp1 := bytecode.Op(proto.Code[5])
	bodyOp2 := bytecode.Op(proto.Code[6])
	if (bodyOp1 != bytecode.ADD && bodyOp1 != bytecode.SUB &&
		bodyOp1 != bytecode.MUL && bodyOp1 != bytecode.DIV) ||
		(bodyOp2 != bytecode.ADD && bodyOp2 != bytecode.SUB &&
			bodyOp2 != bytecode.MUL && bodyOp2 != bytecode.DIV) {
		return shapeInfo{}, false
	}
	// FORPREP sBx=2(body 长度 2)
	if bytecode.SBx(proto.Code[4]) != 2 {
		return shapeInfo{}, false
	}
	// FORLOOP sBx=-3
	if bytecode.SBx(proto.Code[7]) != -3 {
		return shapeInfo{}, false
	}

	aS := bytecode.A(proto.Code[0])
	aInit := bytecode.A(proto.Code[1])
	aLimit := bytecode.A(proto.Code[2])
	aStep := bytecode.A(proto.Code[3])
	aPrep := bytecode.A(proto.Code[4])
	a1A := bytecode.A(proto.Code[5])
	a1B := bytecode.B(proto.Code[5])
	a2A := bytecode.A(proto.Code[6])
	a2B := bytecode.B(proto.Code[6])
	aLoop := bytecode.A(proto.Code[7])
	retA := bytecode.A(proto.Code[8])
	retB := bytecode.B(proto.Code[8])

	if aInit != aS+1 || aLimit != aInit+1 || aStep != aInit+2 ||
		aPrep != aInit || aLoop != aInit {
		return shapeInfo{}, false
	}
	// 两个 body op:A=B=A_s(s = s op K 形态)
	if a1A != aS || a1B != aS || a2A != aS || a2B != aS {
		return shapeInfo{}, false
	}
	if retA != aS || retB != 2 {
		return shapeInfo{}, false
	}

	// body C 必须都是 K 常量(>= 256)且 K 是 number
	b1C := bytecode.C(proto.Code[5])
	b2C := bytecode.C(proto.Code[6])
	if b1C < 256 || b2C < 256 ||
		int(b1C-256) >= len(proto.Consts) || int(b2C-256) >= len(proto.Consts) {
		return shapeInfo{}, false
	}
	kBody1 := proto.Consts[b1C-256]
	kBody2 := proto.Consts[b2C-256]
	if !value.IsNumber(kBody1) || !value.IsNumber(kBody2) {
		return shapeInfo{}, false
	}

	// init/limit/step/s 均 number
	kSIdx := bytecode.Bx(proto.Code[0])
	kInitIdx := bytecode.Bx(proto.Code[1])
	kLimitIdx := bytecode.Bx(proto.Code[2])
	kStepIdx := bytecode.Bx(proto.Code[3])
	if kSIdx >= len(proto.Consts) || kInitIdx >= len(proto.Consts) ||
		kLimitIdx >= len(proto.Consts) || kStepIdx >= len(proto.Consts) {
		return shapeInfo{}, false
	}
	kS := proto.Consts[kSIdx]
	kInit := proto.Consts[kInitIdx]
	kLimit := proto.Consts[kLimitIdx]
	kStep := proto.Consts[kStepIdx]
	if !value.IsNumber(kS) || !value.IsNumber(kInit) ||
		!value.IsNumber(kLimit) || !value.IsNumber(kStep) {
		return shapeInfo{}, false
	}
	if value.AsNumber(kStep) <= 0 {
		return shapeInfo{}, false
	}

	mapSse := func(op bytecode.OpCode) byte {
		switch op {
		case bytecode.ADD:
			return 0x58
		case bytecode.SUB:
			return 0x5C
		case bytecode.MUL:
			return 0x59
		case bytecode.DIV:
			return 0x5E
		}
		return 0
	}

	return shapeInfo{
		ok:          true,
		retA:        uint8(aS),
		retB:        2,
		retPC:       8,
		isForLoop:   true,
		forA:        uint8(aInit),
		forInitK:    uint64(kInit),
		forLimitK:   uint64(kLimit),
		forStepK:    uint64(kStep),
		hasBody:     true, // 复用 hasBody 路径,但 hasBody2 控制走 body2 模板
		hasBody2:    true,
		bodyOp:      mapSse(bodyOp1),
		bodyKValue:  uint64(kBody1),
		bodyOp2:     mapSse(bodyOp2),
		bodyKValue2: uint64(kBody2),
		forBodyAS:   uint8(aS),
		forBodyKS:   uint64(kS),
	}, true
}

// analyzeForLoopBodyForm 识别 PJ3 FORLOOP body 含 reg-K op 形态:
// `function() local s=K_s; for i=K1,K2 do s = s op K3 end; return s end`.
//
// luac 编码(以 `local s=0; for i=1,100 do s = s + 1 end; return s` 为例):
//
//	[0] LOADK    A_s    -K_s    ; s = K_s
//	[1] LOADK    A_init -K_init ; init
//	[2] LOADK    A_init+1 -K_limit ; limit
//	[3] LOADK    A_init+2 -K_step  ; step
//	[4] FORPREP  A_init  sBx=1  ; jmp 到 body
//	[5] ADD/SUB/MUL/DIV A_s A_s C(K_body 索引) ; body = s op K
//	[6] FORLOOP  A_init  sBx=-2 ; jmp 回 [5]
//	[7] RETURN   A_s     B=2    ; return s
//	[8] dead RETURN(可选)
//
// **形态约束**:
//   - proto.Code 长度 8 或 9
//   - [0/1/2/3] 四 LOADK + [4] FORPREP sBx=1 + [5] reg-K arith op
//   - [6] FORLOOP sBx=-2 + [7] RETURN A=A_s B=2 (可选 [8] dead RETURN)
//   - body 是 reg-K(B = A_s = A,C 是 K 索引)+ SSE 白名单 op
//     (ADD/SUB/MUL/DIV)
//   - A_init >= A_s + 1(s 槽位于 for 槽之外,避免覆盖)
func analyzeForLoopBodyForm(proto *bytecode.Proto) (shapeInfo, bool) {
	codeLen := len(proto.Code)
	if codeLen != 8 && codeLen != 9 {
		return shapeInfo{}, false
	}
	if bytecode.Op(proto.Code[0]) != bytecode.LOADK ||
		bytecode.Op(proto.Code[1]) != bytecode.LOADK ||
		bytecode.Op(proto.Code[2]) != bytecode.LOADK ||
		bytecode.Op(proto.Code[3]) != bytecode.LOADK ||
		bytecode.Op(proto.Code[4]) != bytecode.FORPREP ||
		bytecode.Op(proto.Code[6]) != bytecode.FORLOOP ||
		bytecode.Op(proto.Code[7]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	if codeLen == 9 && bytecode.Op(proto.Code[8]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	bodyOp := bytecode.Op(proto.Code[5])
	// body 必须是 SSE 白名单 op
	if bodyOp != bytecode.ADD && bodyOp != bytecode.SUB &&
		bodyOp != bytecode.MUL && bodyOp != bytecode.DIV {
		return shapeInfo{}, false
	}

	// FORPREP sBx=1(jmp 跳过 body 长度 1)
	if bytecode.SBx(proto.Code[4]) != 1 {
		return shapeInfo{}, false
	}
	// FORLOOP sBx=-2(jmp 回 body)
	if bytecode.SBx(proto.Code[6]) != -2 {
		return shapeInfo{}, false
	}

	aS := bytecode.A(proto.Code[0])     // s 槽
	aInit := bytecode.A(proto.Code[1])  // for 槽 base
	aLimit := bytecode.A(proto.Code[2]) // for+1
	aStep := bytecode.A(proto.Code[3])  // for+2
	aPrep := bytecode.A(proto.Code[4])
	aBody := bytecode.A(proto.Code[5])  // body 的 A,= s 槽
	aBodyB := bytecode.B(proto.Code[5]) // body 的 B,= s 槽
	aLoop := bytecode.A(proto.Code[6])
	retA := bytecode.A(proto.Code[7])
	retB := bytecode.B(proto.Code[7])

	if aInit != aS+1 || aLimit != aInit+1 || aStep != aInit+2 ||
		aPrep != aInit || aLoop != aInit {
		return shapeInfo{}, false
	}
	if aBody != aS || aBodyB != aS {
		return shapeInfo{}, false
	}
	// RETURN A=A_s B=2(单返回)
	if retA != aS || retB != 2 {
		return shapeInfo{}, false
	}

	// body 的 C 必须是 K 常量(>= 256),且 K 是 number
	bodyC := bytecode.C(proto.Code[5])
	if bodyC < 256 || int(bodyC-256) >= len(proto.Consts) {
		return shapeInfo{}, false
	}
	kBody := proto.Consts[bodyC-256]
	if !value.IsNumber(kBody) {
		return shapeInfo{}, false
	}

	// init / limit / step / s 必须都是 number K
	kSIdx := bytecode.Bx(proto.Code[0])
	kInitIdx := bytecode.Bx(proto.Code[1])
	kLimitIdx := bytecode.Bx(proto.Code[2])
	kStepIdx := bytecode.Bx(proto.Code[3])
	if kSIdx >= len(proto.Consts) || kInitIdx >= len(proto.Consts) ||
		kLimitIdx >= len(proto.Consts) || kStepIdx >= len(proto.Consts) {
		return shapeInfo{}, false
	}
	kS := proto.Consts[kSIdx]
	kInit := proto.Consts[kInitIdx]
	kLimit := proto.Consts[kLimitIdx]
	kStep := proto.Consts[kStepIdx]
	if !value.IsNumber(kS) || !value.IsNumber(kInit) ||
		!value.IsNumber(kLimit) || !value.IsNumber(kStep) {
		return shapeInfo{}, false
	}

	// step > 0 仅(jcc=ja 退出)
	if value.AsNumber(kStep) <= 0 {
		return shapeInfo{}, false
	}

	// 映射 SSE op
	var sseOp byte
	switch bodyOp {
	case bytecode.ADD:
		sseOp = 0x58 // ADDSD
	case bytecode.SUB:
		sseOp = 0x5C
	case bytecode.MUL:
		sseOp = 0x59
	case bytecode.DIV:
		sseOp = 0x5E
	}

	return shapeInfo{
		ok:         true,
		retA:       uint8(aS),
		retB:       2, // return s
		retPC:      7,
		isForLoop:  true,
		forA:       uint8(aInit),
		forInitK:   uint64(kInit),
		forLimitK:  uint64(kLimit),
		forStepK:   uint64(kStep),
		hasBody:    true,
		bodyOp:     sseOp,
		bodyKValue: uint64(kBody),
		forBodyAS:  uint8(aS),
		forBodyKS:  uint64(kS),
	}, true
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
// **形态约束**:
//   - proto.Code 长度 6 或 7(尾部可选 dead RETURN)
//   - [0] LOADK A_init -kInit
//   - [1] LOADK A_init+1 -kLimit **或** MOVE A_init+1 limitReg
//     (reg-limit hot path:`for i=1, n do end` luac 编 MOVE)
//   - [2] LOADK A_init+2 -kStep
//   - [3] FORPREP A_init sBx=0(空 body 时 luac 编 0)
//   - [4] FORLOOP A_init sBx=-1(回边跳自己)
//   - [5] RETURN A=0 B=1(空 return)
//   - K[kInit / kStep] 必须都是 number(否则降级 host);LOADK 形态下
//     K[kLimit] 也必须是 number;MOVE 形态下 limitReg 运行期 IsNumber
//     guard
//
// **当前已接入主路径**(承 Compile 端):
//   - LOADK limit 形态:69/83 字节模板(空 body 全常量),已实测
//     7-25x over gopher-lua
//   - MOVE limit 形态:117 字节模板(IsNumber guard + deopt 调
//     host.ForPrep raise byte-equal P1),hot path 真接入完整
//
// **不支持**(留 PJ3 真接入扩展):
//   - body 非空(需 inline body opcodes + 寄存器分配)
//   - 嵌套 for / 含 break(JMP)
//   - 非默认 step(step=1 隐含;非默认编码 step 仍走本路径,因 step 也是 K)
func analyzeForLoopForm(proto *bytecode.Proto) (shapeInfo, bool) {
	codeLen := len(proto.Code)
	if codeLen != 6 && codeLen != 7 {
		return shapeInfo{}, false
	}
	// [0/1/2] LOADK / [3] FORPREP / [4] FORLOOP / [5] RETURN
	// **limit 支持 LOADK / MOVE / GETUPVAL**:
	//   - LOADK:常量 limit(`for i=1,100 do end`)
	//   - MOVE :reg-limit hot path(`for i=1,n do end`,n=参数 reg)
	//   - GETUPVAL:upvalue-limit(closure capture,`local n=100; local
	//     function f() for i=1,n do end end`,n 是 upvalue)
	if bytecode.Op(proto.Code[0]) != bytecode.LOADK ||
		(bytecode.Op(proto.Code[1]) != bytecode.LOADK &&
			bytecode.Op(proto.Code[1]) != bytecode.MOVE &&
			bytecode.Op(proto.Code[1]) != bytecode.GETUPVAL) ||
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

	// init / step:必须 LOADK + K 是 number
	kInitIdx := bytecode.Bx(proto.Code[0])
	kStepIdx := bytecode.Bx(proto.Code[2])
	if kInitIdx >= len(proto.Consts) || kStepIdx >= len(proto.Consts) {
		return shapeInfo{}, false
	}
	kInit := proto.Consts[kInitIdx]
	kStep := proto.Consts[kStepIdx]
	if !value.IsNumber(kInit) || !value.IsNumber(kStep) {
		return shapeInfo{}, false
	}

	// **step > 0 才支持本简化模板**(jcc 选 ja:idx > limit 退出)。
	// step ≤ 0 或负 step 留 PJ3+ 扩(jcc 选 jb:idx < limit 退出)。
	stepF := value.AsNumber(kStep)
	if stepF <= 0 {
		return shapeInfo{}, false
	}

	// limit:LOADK(常量)或 MOVE(reg-limit hot path)
	si := shapeInfo{
		ok:        true,
		retA:      0, // RETURN A=0
		retB:      1, // 空 return
		retPC:     5,
		isForLoop: true,
		forA:      uint8(aInit),
		forInitK:  uint64(kInit),
		forStepK:  uint64(kStep),
	}
	if bytecode.Op(proto.Code[1]) == bytecode.LOADK {
		kLimitIdx := bytecode.Bx(proto.Code[1])
		if kLimitIdx >= len(proto.Consts) {
			return shapeInfo{}, false
		}
		kLimit := proto.Consts[kLimitIdx]
		if !value.IsNumber(kLimit) {
			return shapeInfo{}, false
		}
		si.forLimitK = uint64(kLimit)
		si.forLimitIsReg = false
	} else if bytecode.Op(proto.Code[1]) == bytecode.MOVE {
		// **MOVE A B reg-limit 形态**(luac 编 `for i=1,n do end` 时
		// limit=n 用 MOVE)。字节级模板 EmitForLoopRegLimit 已实装,deopt
		// 路径调 host.ForPrep raise(`'for' limit must be a number`)
		// byte-equal 解释器(若 R(limitReg) 非 number)。
		moveB := bytecode.B(proto.Code[1])
		if moveB > 254 {
			return shapeInfo{}, false
		}
		si.forLimitReg = uint8(moveB)
		si.forLimitIsReg = true
	} else {
		// **GETUPVAL A B upvalue-limit 形态**(closure capture):luac 编
		// closure 内 `for i=1,upval_n do end` 时 [1] = GETUPVAL A B,
		// A=A_init+1 / B=upvalue 索引。Run 端调 host.GetUpval(B) 取值后
		// 直接经 host.SetReg 写 R(A_init+1) 槽,然后走 reg-limit 模板。
		// 因为 host.GetUpval 后写槽位则 limit 已 number(若 upvalue 是
		// number);否则 reg-limit 模板的 IsNumber guard 自动触发 deopt。
		guvB := bytecode.B(proto.Code[1])
		// 上限 254:`uint8(guvB) + 1` 不溢出。255 → uint8(255)+1 = 0,而
		// 0 在该字段语义里表示「不走 upval 路径」,Run 端跳过 host.GetUpval +
		// SetReg → reg-limit 模板读到未填充 R(forLimitReg) → 错误循环界或
		// 误 deopt。触达极低(需第 256 个 upvalue 作 FORLOOP 上界),但属
		// 边界自相矛盾。
		if guvB > 254 {
			return shapeInfo{}, false
		}
		si.forLimitReg = uint8(aLimit)        // 目标槽 = R(A_init+1)
		si.forLimitIsReg = true               // 仍走 reg-limit 模板
		si.forLimitUpvalIdx = uint8(guvB) + 1 // 1-based(0 = 不走 upval)
	}

	return si, true
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

// analyzeCallVoidForm 识别 PJ5 CALL void 简化形态(承
// docs/design/p4-method-jit/05-system-pipeline.md §4.3 + 06-backends.md §3.5):
// `function(g) g() end` / `function() noop() end` 类 — 单 BB 三 op,call
// 前置 op 是 MOVE(被调位在参数槽,函数内 parameter 形态)或 GETUPVAL
// (闭包内调用外层 local known fn,upvalue 形态)。
//
// luac 编译形态(两形态):
//
//	形态 A:`function(g) g() end`(parameter callee)
//	  [0] MOVE     A=被调位 B=参数源位
//	  [1] CALL     A=被调位 B=1(0 参) C=1(0 返)
//	  [2] RETURN   A=0 B=1(0 返)
//
//	形态 B:`local function noop()...end; local function invoker() noop() end`
//	  [0] GETUPVAL A=被调位 B=upvalue 索引
//	  [1] CALL     A=被调位 B=1 C=1
//	  [2] RETURN   A=0 B=1
//
// **触发条件**(两形态共同 + 各自):
//   - Code 长度 = 3
//   - [0] = MOVE 或 GETUPVAL,[0].A 与 [1].A(CALL.A)一致
//   - [1] = CALL,CALL.B == 1(0 参)+ CALL.C == 1(0 返)
//   - [2] = RETURN,RETURN.B == 1(0 返值)
//   - reg / upvalue 索引在 [0,254] 范围
//
// **PJ5 简化形态范围**:Run 端 prelude 路径根据 isCallUpval 分流:
//   - 形态 A(isCallUpval=false):host.GetReg(MOVE.B) + SetReg(MOVE.A)
//     完成 MOVE 预处理
//   - 形态 B(isCallUpval=true):host.GetUpval(base, GETUPVAL.B) +
//     SetReg(GETUPVAL.A) 完成 upvalue 取值预处理
//     然后调 host.CallBaseline(base, callPC, callA, callB, callC) +
//     host.DoReturn 弹帧。
//
// 失败任一条件返 (shapeInfo{}, false) — 走 analyzeShape 主分流(可能匹配
// 其它形态如 GETUPVAL+RETURN A 2 单 op 形态守卫 retB=2 会拒 setter 形态)。
// decodeArgFromOp 解码 proto.Code[codeIdx] 处的 LOADK / MOVE op,装入
// argIsK + argK / argReg 三态(供 PJ5 CALL/TAILCALL 形态识别复用)。
//   - 期望 op.A == expectA(参数槽位 R(expectA))
//   - LOADK:解 Bx 索引 proto.Consts,装 argK + argIsK=true
//   - MOVE:解 B 装 argReg + argIsK=false(B 需 ≤ 254 防御性兜底)
//
// 失败任一条件返 false — caller 走 shapeInfo{}, false。
func decodeArgFromOp(proto *bytecode.Proto, codeIdx int, expectA int,
	argIsK *bool, argK *uint64, argReg *uint8) bool {
	op := bytecode.Op(proto.Code[codeIdx])
	if op != bytecode.LOADK && op != bytecode.MOVE {
		return false
	}
	opA := bytecode.A(proto.Code[codeIdx])
	if opA != expectA {
		return false
	}
	if op == bytecode.LOADK {
		bx := bytecode.Bx(proto.Code[codeIdx])
		if bx < 0 || bx >= len(proto.Consts) {
			return false
		}
		*argK = uint64(proto.Consts[bx])
		*argIsK = true
	} else {
		b := bytecode.B(proto.Code[codeIdx])
		if b > 254 {
			return false
		}
		*argReg = uint8(b)
		*argIsK = false
	}
	return true
}

func analyzeCallVoidForm(proto *bytecode.Proto) (shapeInfo, bool) {
	codeLen := len(proto.Code)
	if codeLen < 3 || codeLen > 11 {
		return shapeInfo{}, false
	}
	op0 := bytecode.Op(proto.Code[0])
	if op0 != bytecode.MOVE && op0 != bytecode.GETUPVAL {
		return shapeInfo{}, false
	}
	op0A := bytecode.A(proto.Code[0])
	op0B := bytecode.B(proto.Code[0])
	if op0A > 254 || op0B > 254 {
		return shapeInfo{}, false
	}

	// 长度 3:0 参 0 返(MOVE/GETUPVAL + CALL B=1 C=1 + RETURN B=1)
	// 长度 4:三种子形态
	//   - [1]=CALL B=1 C=2 + [2]=RETURN B=2 + [3]=dead RETURN:0 参 1 返(getter)
	//   - [1]=LOADK,[2]=CALL B=2 C=1,[3]=RETURN B=1:1 K 参 0 返(setter)
	//   - [1]=MOVE,[2]=CALL B=2 C=1,[3]=RETURN B=1:1 reg 参 0 返(setter)
	// 长度 5:2 参 0 返 — GETUPVAL/MOVE + (LOADK|MOVE) + (LOADK|MOVE) + CALL B=3 C=1 + RETURN B=1
	//   四组合 K+K / K+R / R+K / R+R(均 setter,callArgCount=2)
	var callIdx, retIdx int
	var argK uint64
	var argReg uint8
	var arg2K uint64
	var arg2Reg uint8
	var arg2IsK bool
	var arg3K uint64
	var arg3Reg uint8
	var arg3IsK bool
	var arg4K uint64
	var arg4Reg uint8
	var arg4IsK bool
	var arg5K uint64
	var arg5Reg uint8
	var arg5IsK bool
	var arg6K uint64
	var arg6Reg uint8
	var arg6IsK bool
	var arg7K uint64
	var arg7Reg uint8
	var arg7IsK bool
	var argCount uint8
	var argIsK bool
	switch codeLen {
	case 3:
		callIdx = 1
		retIdx = 2
		argCount = 0
	case 4:
		secondOp := bytecode.Op(proto.Code[1])
		switch secondOp {
		case bytecode.CALL:
			callIdx = 1
			retIdx = 2
			argCount = 0
			if bytecode.Op(proto.Code[3]) != bytecode.RETURN {
				return shapeInfo{}, false
			}
		case bytecode.LOADK:
			lkA := bytecode.A(proto.Code[1])
			lkBx := bytecode.Bx(proto.Code[1])
			if lkA != op0A+1 {
				return shapeInfo{}, false
			}
			if lkBx < 0 || lkBx >= len(proto.Consts) {
				return shapeInfo{}, false
			}
			argK = uint64(proto.Consts[lkBx])
			argIsK = true
			callIdx = 2
			retIdx = 3
			argCount = 1
		case bytecode.MOVE:
			mvA := bytecode.A(proto.Code[1])
			mvB := bytecode.B(proto.Code[1])
			if mvA != op0A+1 {
				return shapeInfo{}, false
			}
			if mvB > 254 {
				return shapeInfo{}, false
			}
			argReg = uint8(mvB)
			argIsK = false
			callIdx = 2
			retIdx = 3
			argCount = 1
		default:
			return shapeInfo{}, false
		}
	case 5:
		// 长度 5 子分支区分:
		//   - getter 1 K/reg 参 1 返:[0] MOVE/GETUPVAL,[1] LOADK/MOVE,
		//     [2] CALL B=2 C=2,[3] RETURN A=callA B=2,[4] 隐式 RETURN B=1
		//   - setter 2 参 0 返:[0] MOVE/GETUPVAL,[1] LOADK/MOVE,[2] LOADK/MOVE,
		//     [3] CALL B=3 C=1,[4] RETURN B=1
		// 关键区分位:Code[2] 是 CALL 即 getter 1 参 / 否则 setter 2 参。
		if bytecode.Op(proto.Code[2]) == bytecode.CALL {
			// getter 1 参 1 返:[1] LOADK/MOVE,[2] CALL B=2 C=2,[3] RETURN A=callA B=2,
			// [4] 隐式 RETURN B=1
			secondOp := bytecode.Op(proto.Code[1])
			switch secondOp {
			case bytecode.LOADK:
				lkA := bytecode.A(proto.Code[1])
				lkBx := bytecode.Bx(proto.Code[1])
				if lkA != op0A+1 {
					return shapeInfo{}, false
				}
				if lkBx < 0 || lkBx >= len(proto.Consts) {
					return shapeInfo{}, false
				}
				argK = uint64(proto.Consts[lkBx])
				argIsK = true
			case bytecode.MOVE:
				mvA := bytecode.A(proto.Code[1])
				mvB := bytecode.B(proto.Code[1])
				if mvA != op0A+1 {
					return shapeInfo{}, false
				}
				if mvB > 254 {
					return shapeInfo{}, false
				}
				argReg = uint8(mvB)
				argIsK = false
			default:
				return shapeInfo{}, false
			}
			callIdx = 2
			retIdx = 3
			argCount = 1
			// 校验 [4] 隐式 RETURN B=1
			implRet := proto.Code[4]
			if bytecode.Op(implRet) != bytecode.RETURN || bytecode.B(implRet) != 1 {
				return shapeInfo{}, false
			}
		} else {
			// setter 2 参 0 返:GETUPVAL/MOVE + (LOADK|MOVE) + (LOADK|MOVE) + CALL + RETURN
			// 四组合:K+K / K+R / R+K / R+R(承同 PJ5 真接入主路径形态学扩展)
			secondOp := bytecode.Op(proto.Code[1])
			thirdOp := bytecode.Op(proto.Code[2])
			if (secondOp != bytecode.LOADK && secondOp != bytecode.MOVE) ||
				(thirdOp != bytecode.LOADK && thirdOp != bytecode.MOVE) {
				return shapeInfo{}, false
			}
			op2A := bytecode.A(proto.Code[1])
			op3A := bytecode.A(proto.Code[2])
			if op2A != op0A+1 || op3A != op0A+2 {
				return shapeInfo{}, false
			}
			// 第一参装载
			if secondOp == bytecode.LOADK {
				lk1Bx := bytecode.Bx(proto.Code[1])
				if lk1Bx < 0 || lk1Bx >= len(proto.Consts) {
					return shapeInfo{}, false
				}
				argK = uint64(proto.Consts[lk1Bx])
				argIsK = true
			} else {
				mv1B := bytecode.B(proto.Code[1])
				if mv1B > 254 {
					return shapeInfo{}, false
				}
				argReg = uint8(mv1B)
				argIsK = false
			}
			// 第二参装载
			if thirdOp == bytecode.LOADK {
				lk2Bx := bytecode.Bx(proto.Code[2])
				if lk2Bx < 0 || lk2Bx >= len(proto.Consts) {
					return shapeInfo{}, false
				}
				arg2K = uint64(proto.Consts[lk2Bx])
				arg2IsK = true
			} else {
				mv2B := bytecode.B(proto.Code[2])
				if mv2B > 254 {
					return shapeInfo{}, false
				}
				arg2Reg = uint8(mv2B)
				arg2IsK = false
			}
			callIdx = 3
			retIdx = 4
			argCount = 2
		}
	case 6:
		// 长度 6 三种子形态(区分键:
		//   Code[1] 是 CALL → 0 参 N=2 返值 getter(callee 调用 + 2 个 MOVE 拷贝 + RETURN B=3)
		//   Code[3] 是 CALL → getter 2 参 1 返
		//   否则 Code[3] 是装载 → setter 3 参 0 返)
		if bytecode.Op(proto.Code[1]) == bytecode.CALL {
			// 0 参 N=2 返值 getter:[0] MOVE/GETUPVAL,[1] CALL B=1 C=3,
			// [2] MOVE A=callA+0+2 B=callA+0,[3] MOVE A=callA+1+2 B=callA+1,
			// [4] RETURN A=callA+2 B=3,[5] 隐式 RETURN B=1
			//
			// Run 端 prelude 不执行 MOVE 拷贝(直接从 CallBaseline 落 R(callA..)),
			// 仍调 host.DoReturn(retA=callA, retB=3)由 host 多值路径处理 — 但因
			// luac 编 RETURN.A=callA+2 而非 callA,Run 必须先做 N 个 MOVE 拷贝
			// (R(callA+nret+k) ← R(callA+k))再调 DoReturn(retA=callA+nret, retB=nret+1)
			// 以保留 byte-equal。
			callIdx = 1
			retIdx = 4
			argCount = 0
			// 校验 [5] 隐式 RETURN B=1
			implRet := proto.Code[5]
			if bytecode.Op(implRet) != bytecode.RETURN || bytecode.B(implRet) != 1 {
				return shapeInfo{}, false
			}
		} else if bytecode.Op(proto.Code[3]) == bytecode.CALL {
			// getter 2 参 1 返
			if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) ||
				!decodeArgFromOp(proto, 2, op0A+2, &arg2IsK, &arg2K, &arg2Reg) {
				return shapeInfo{}, false
			}
			callIdx = 3
			retIdx = 4
			argCount = 2
			// 校验 [5] 隐式 RETURN B=1
			implRet := proto.Code[5]
			if bytecode.Op(implRet) != bytecode.RETURN || bytecode.B(implRet) != 1 {
				return shapeInfo{}, false
			}
		} else {
			// setter 3 参 0 返
			if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) ||
				!decodeArgFromOp(proto, 2, op0A+2, &arg2IsK, &arg2K, &arg2Reg) ||
				!decodeArgFromOp(proto, 3, op0A+3, &arg3IsK, &arg3K, &arg3Reg) {
				return shapeInfo{}, false
			}
			callIdx = 4
			retIdx = 5
			argCount = 3
		}
	case 7:
		// 长度 7 四种子形态(区分键:
		//   Code[1] 是 CALL → 0 参 N=3 返值 getter
		//   Code[2] 是 CALL → 1 K/reg 参 N=2 返值 getter
		//   Code[5] 是 CALL → setter 4 参 0 返
		//   否则 Code[4] 是 CALL → getter 3 参 1 返)
		if bytecode.Op(proto.Code[1]) == bytecode.CALL {
			// 0 参 N=3 返值 getter:[0] MOVE/GETUPVAL,[1] CALL B=1 C=4,
			// [2..4] MOVE,[5] RETURN A=callA+3 B=4,[6] 隐式 RETURN B=1
			callIdx = 1
			retIdx = 5
			argCount = 0
			// 校验 [6] 隐式 RETURN B=1
			implRet := proto.Code[6]
			if bytecode.Op(implRet) != bytecode.RETURN || bytecode.B(implRet) != 1 {
				return shapeInfo{}, false
			}
		} else if bytecode.Op(proto.Code[2]) == bytecode.CALL {
			// 1 K/reg 参 N=2 返值 getter:[0] MOVE/GETUPVAL,[1] (LOADK|MOVE),
			// [2] CALL B=2 C=3,[3] MOVE,[4] MOVE,[5] RETURN A=callA+2 B=3,[6] 隐式 RETURN B=1
			if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) {
				return shapeInfo{}, false
			}
			callIdx = 2
			retIdx = 5
			argCount = 1
			// 校验 [6] 隐式 RETURN B=1
			implRet := proto.Code[6]
			if bytecode.Op(implRet) != bytecode.RETURN || bytecode.B(implRet) != 1 {
				return shapeInfo{}, false
			}
		} else if bytecode.Op(proto.Code[5]) == bytecode.CALL {
			// setter 4 参 0 返:[0] MOVE/GETUPVAL,[1..4] (LOADK|MOVE),
			// [5] CALL B=5 C=1,[6] RETURN B=1
			if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) ||
				!decodeArgFromOp(proto, 2, op0A+2, &arg2IsK, &arg2K, &arg2Reg) ||
				!decodeArgFromOp(proto, 3, op0A+3, &arg3IsK, &arg3K, &arg3Reg) ||
				!decodeArgFromOp(proto, 4, op0A+4, &arg4IsK, &arg4K, &arg4Reg) {
				return shapeInfo{}, false
			}
			callIdx = 5
			retIdx = 6
			argCount = 4
		} else {
			// getter 3 参 1 返:[0] MOVE/GETUPVAL,[1..3] (LOADK|MOVE),
			// [4] CALL B=4 C=2,[5] RETURN A=callA B=2,[6] 隐式 RETURN B=1
			if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) ||
				!decodeArgFromOp(proto, 2, op0A+2, &arg2IsK, &arg2K, &arg2Reg) ||
				!decodeArgFromOp(proto, 3, op0A+3, &arg3IsK, &arg3K, &arg3Reg) {
				return shapeInfo{}, false
			}
			callIdx = 4
			retIdx = 5
			argCount = 3
			// 校验 [6] 隐式 RETURN B=1
			implRet := proto.Code[6]
			if bytecode.Op(implRet) != bytecode.RETURN || bytecode.B(implRet) != 1 {
				return shapeInfo{}, false
			}
		}
	case 8:
		// 长度 8 四种子形态(区分键:
		//   Code[2] 是 CALL → 1 K/reg 参 N=3 返值 getter
		//   Code[5] 是 CALL → getter 4 参 1 返
		//   Code[6] 是 CALL → setter 5 参 0 返)
		if bytecode.Op(proto.Code[2]) == bytecode.CALL {
			// 1 K/reg 参 N=3 返值 getter:[0] MOVE/GETUPVAL,[1] (LOADK|MOVE),
			// [2] CALL B=2 C=4,[3..5] MOVE,[6] RETURN A=callA+3 B=4,[7] 隐式 RETURN B=1
			if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) {
				return shapeInfo{}, false
			}
			callIdx = 2
			retIdx = 6
			argCount = 1
			// 校验 [7] 隐式 RETURN B=1
			implRet := proto.Code[7]
			if bytecode.Op(implRet) != bytecode.RETURN || bytecode.B(implRet) != 1 {
				return shapeInfo{}, false
			}
		} else if bytecode.Op(proto.Code[5]) == bytecode.CALL {
			// getter 4 参 1 返:[0] MOVE/GETUPVAL,[1..4] (LOADK|MOVE),
			// [5] CALL B=5 C=2,[6] RETURN A=callA B=2,[7] 隐式 RETURN B=1
			if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) ||
				!decodeArgFromOp(proto, 2, op0A+2, &arg2IsK, &arg2K, &arg2Reg) ||
				!decodeArgFromOp(proto, 3, op0A+3, &arg3IsK, &arg3K, &arg3Reg) ||
				!decodeArgFromOp(proto, 4, op0A+4, &arg4IsK, &arg4K, &arg4Reg) {
				return shapeInfo{}, false
			}
			callIdx = 5
			retIdx = 6
			argCount = 4
			// 校验 [7] 隐式 RETURN B=1
			implRet := proto.Code[7]
			if bytecode.Op(implRet) != bytecode.RETURN || bytecode.B(implRet) != 1 {
				return shapeInfo{}, false
			}
		} else if bytecode.Op(proto.Code[6]) == bytecode.CALL {
			// setter 5 参 0 返:[0] MOVE/GETUPVAL,[1..5] (LOADK|MOVE),
			// [6] CALL B=6 C=1,[7] RETURN B=1
			if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) ||
				!decodeArgFromOp(proto, 2, op0A+2, &arg2IsK, &arg2K, &arg2Reg) ||
				!decodeArgFromOp(proto, 3, op0A+3, &arg3IsK, &arg3K, &arg3Reg) ||
				!decodeArgFromOp(proto, 4, op0A+4, &arg4IsK, &arg4K, &arg4Reg) ||
				!decodeArgFromOp(proto, 5, op0A+5, &arg5IsK, &arg5K, &arg5Reg) {
				return shapeInfo{}, false
			}
			callIdx = 6
			retIdx = 7
			argCount = 5
		} else {
			return shapeInfo{}, false
		}
	case 9:
		// 长度 9 两种子形态(区分键:Code[6] 是 CALL):
		//   - getter 5 参 1 返:Code[6]=CALL B=6 C=2 + Code[7]=RETURN A=callA B=2 + Code[8]=隐式
		//   - setter 6 参 0 返:Code[7]=CALL B=7 C=1 + Code[8]=RETURN B=1
		if bytecode.Op(proto.Code[6]) == bytecode.CALL {
			// getter 5 参 1 返
			if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) ||
				!decodeArgFromOp(proto, 2, op0A+2, &arg2IsK, &arg2K, &arg2Reg) ||
				!decodeArgFromOp(proto, 3, op0A+3, &arg3IsK, &arg3K, &arg3Reg) ||
				!decodeArgFromOp(proto, 4, op0A+4, &arg4IsK, &arg4K, &arg4Reg) ||
				!decodeArgFromOp(proto, 5, op0A+5, &arg5IsK, &arg5K, &arg5Reg) {
				return shapeInfo{}, false
			}
			callIdx = 6
			retIdx = 7
			argCount = 5
			// 校验 [8] 隐式 RETURN B=1
			implRet := proto.Code[8]
			if bytecode.Op(implRet) != bytecode.RETURN || bytecode.B(implRet) != 1 {
				return shapeInfo{}, false
			}
		} else if bytecode.Op(proto.Code[7]) == bytecode.CALL {
			// setter 6 参 0 返
			if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) ||
				!decodeArgFromOp(proto, 2, op0A+2, &arg2IsK, &arg2K, &arg2Reg) ||
				!decodeArgFromOp(proto, 3, op0A+3, &arg3IsK, &arg3K, &arg3Reg) ||
				!decodeArgFromOp(proto, 4, op0A+4, &arg4IsK, &arg4K, &arg4Reg) ||
				!decodeArgFromOp(proto, 5, op0A+5, &arg5IsK, &arg5K, &arg5Reg) ||
				!decodeArgFromOp(proto, 6, op0A+6, &arg6IsK, &arg6K, &arg6Reg) {
				return shapeInfo{}, false
			}
			callIdx = 7
			retIdx = 8
			argCount = 6
		} else {
			return shapeInfo{}, false
		}
	case 10:
		// 长度 10:setter 7 参 0 返(Code[8]=CALL B=8 C=1)/ getter 6 参 1 返(Code[7]=CALL B=7 C=2)
		// 区分键 Code[7] vs Code[8] 是 CALL
		if bytecode.Op(proto.Code[7]) == bytecode.CALL {
			// getter 6 参 1 返(承前)
			if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) ||
				!decodeArgFromOp(proto, 2, op0A+2, &arg2IsK, &arg2K, &arg2Reg) ||
				!decodeArgFromOp(proto, 3, op0A+3, &arg3IsK, &arg3K, &arg3Reg) ||
				!decodeArgFromOp(proto, 4, op0A+4, &arg4IsK, &arg4K, &arg4Reg) ||
				!decodeArgFromOp(proto, 5, op0A+5, &arg5IsK, &arg5K, &arg5Reg) ||
				!decodeArgFromOp(proto, 6, op0A+6, &arg6IsK, &arg6K, &arg6Reg) {
				return shapeInfo{}, false
			}
			callIdx = 7
			retIdx = 8
			argCount = 6
			implRet := proto.Code[9]
			if bytecode.Op(implRet) != bytecode.RETURN || bytecode.B(implRet) != 1 {
				return shapeInfo{}, false
			}
		} else if bytecode.Op(proto.Code[8]) == bytecode.CALL {
			// setter 7 参 0 返
			if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) ||
				!decodeArgFromOp(proto, 2, op0A+2, &arg2IsK, &arg2K, &arg2Reg) ||
				!decodeArgFromOp(proto, 3, op0A+3, &arg3IsK, &arg3K, &arg3Reg) ||
				!decodeArgFromOp(proto, 4, op0A+4, &arg4IsK, &arg4K, &arg4Reg) ||
				!decodeArgFromOp(proto, 5, op0A+5, &arg5IsK, &arg5K, &arg5Reg) ||
				!decodeArgFromOp(proto, 6, op0A+6, &arg6IsK, &arg6K, &arg6Reg) ||
				!decodeArgFromOp(proto, 7, op0A+7, &arg7IsK, &arg7K, &arg7Reg) {
				return shapeInfo{}, false
			}
			callIdx = 8
			retIdx = 9
			argCount = 7
		} else {
			return shapeInfo{}, false
		}
	case 11:
		// 长度 11:getter 7 参 1 返(Code[8]=CALL B=8 C=2 + RETURN A=callA B=2 + 隐式 RETURN B=1)
		if bytecode.Op(proto.Code[8]) != bytecode.CALL {
			return shapeInfo{}, false
		}
		if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) ||
			!decodeArgFromOp(proto, 2, op0A+2, &arg2IsK, &arg2K, &arg2Reg) ||
			!decodeArgFromOp(proto, 3, op0A+3, &arg3IsK, &arg3K, &arg3Reg) ||
			!decodeArgFromOp(proto, 4, op0A+4, &arg4IsK, &arg4K, &arg4Reg) ||
			!decodeArgFromOp(proto, 5, op0A+5, &arg5IsK, &arg5K, &arg5Reg) ||
			!decodeArgFromOp(proto, 6, op0A+6, &arg6IsK, &arg6K, &arg6Reg) ||
			!decodeArgFromOp(proto, 7, op0A+7, &arg7IsK, &arg7K, &arg7Reg) {
			return shapeInfo{}, false
		}
		callIdx = 8
		retIdx = 9
		argCount = 7
		// 校验 [10] 隐式 RETURN B=1
		implRet := proto.Code[10]
		if bytecode.Op(implRet) != bytecode.RETURN || bytecode.B(implRet) != 1 {
			return shapeInfo{}, false
		}
	}

	if bytecode.Op(proto.Code[callIdx]) != bytecode.CALL ||
		bytecode.Op(proto.Code[retIdx]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	clA := bytecode.A(proto.Code[callIdx])
	clB := bytecode.B(proto.Code[callIdx])
	clC := bytecode.C(proto.Code[callIdx])
	rtA := bytecode.A(proto.Code[retIdx])
	rtB := bytecode.B(proto.Code[retIdx])
	if clA != op0A {
		return shapeInfo{}, false
	}
	// CALL.B = argCount + 1
	if int(clB) != int(argCount)+1 {
		return shapeInfo{}, false
	}
	// 返值检查:CALL.C/RETURN.B 共 3 形态:
	//   - setter:CALL.C=1(0 返值)+ RETURN.B=1(0 返值)+ retA=0
	//   - getter 1 返:CALL.C=2(1 返值)+ RETURN.B=2(1 返值)+ RETURN.A=callA
	//   - N 返值 getter(N>=2):CALL.C=N+1 + RETURN.B=N+1 + RETURN.A=callA+N
	//     (luac 编 N 个 MOVE 把 R(callA..callA+N-1)拷到 R(callA+N..callA+2N-1)再 RETURN)
	var retACalc, retBCalc uint8
	var multiRet uint8
	if clC == 1 && rtB == 1 {
		// setter 形态
		retACalc = 0
		retBCalc = 1
	} else if clC == 2 && rtB == 2 {
		// getter 1 返形态:RETURN.A 必须 = callA(被调返回值落 R(callA))
		if rtA != clA {
			return shapeInfo{}, false
		}
		retACalc = uint8(rtA)
		retBCalc = 2
	} else if clC >= 3 && int(rtB) == int(clC) {
		// N>=2 返值 getter:RETURN.A 必须 = callA + (clC-1)= callA + nret
		// argCount 可以是 0(无参)或 >=1(含参,如 `local a,b=f(arg); return a,b`)
		nret := clC - 1
		if rtA != clA+nret {
			return shapeInfo{}, false
		}
		// 校验中间 N 个 MOVE 拷贝(luac 编 R(callA+nret+k) ← R(callA+k))
		for k := 0; k < nret; k++ {
			mv := proto.Code[callIdx+1+k]
			if bytecode.Op(mv) != bytecode.MOVE {
				return shapeInfo{}, false
			}
			if bytecode.A(mv) != clA+nret+k || bytecode.B(mv) != clA+k {
				return shapeInfo{}, false
			}
		}
		retACalc = uint8(rtA)
		retBCalc = uint8(rtB)
		multiRet = uint8(nret)
	} else {
		return shapeInfo{}, false
	}
	return shapeInfo{
		ok:             true,
		retA:           retACalc,
		retB:           retBCalc,
		retPC:          uint8(retIdx),
		preludeOp:      uint8(bytecode.CALL),
		preludeArg:     uint32(op0B),
		isCallVoid:     true,
		isCallUpval:    op0 == bytecode.GETUPVAL,
		callA:          uint8(clA),
		callB:          uint8(clB),
		callC:          uint8(clC),
		callArgCount:   argCount,
		callMultiRet:   multiRet,
		callArg1IsK:    argIsK,
		callArg1K:      argK,
		callArg1RegSrc: argReg,
		callArg2IsK:    arg2IsK,
		callArg2K:      arg2K,
		callArg2RegSrc: arg2Reg,
		callArg3IsK:    arg3IsK,
		callArg3K:      arg3K,
		callArg3RegSrc: arg3Reg,
		callArg4IsK:    arg4IsK,
		callArg4K:      arg4K,
		callArg4RegSrc: arg4Reg,
		callArg5IsK:    arg5IsK,
		callArg5K:      arg5K,
		callArg5RegSrc: arg5Reg,
		callArg6IsK:    arg6IsK,
		callArg6K:      arg6K,
		callArg6RegSrc: arg6Reg,
		callArg7IsK:    arg7IsK,
		callArg7K:      arg7K,
		callArg7RegSrc: arg7Reg,
	}, true
}

// analyzeTailCallForm 识别 PJ5 TAILCALL 形态(承
// docs/design/p4-method-jit/05-system-pipeline.md §4.3 + 06-backends.md §3.5):
// `function() return f() end` / `function() return f(K) end` 等 — 单值
// CallExpr 作 return 唯一表达式被 luac 翻成 TAILCALL + 尾随 RETURN B=0 +
// 隐式 RETURN(stmtReturn 单 CallExpr 快路径,frontend/compile/stmt.go::stmtReturn)。
//
// luac 编译形态(0/1 K/1 reg/2 K 参 × MOVE/GETUPVAL 共 8 子形态;TAILCALL.C
// 恒 0 即「返回值到 top」):
//
//	形态 TA0:`function(g) return g() end`(parameter callee,0 参)
//	  [0] MOVE     A=callA B=被调源 reg
//	  [1] TAILCALL A=callA B=1 C=0
//	  [2] RETURN   A=callA B=0 (dead,to-top)
//	  [3] RETURN   A=0 B=1     (隐式)
//
//	形态 TB0:`local function f()...; local function bounce() return f() end`
//	  [0] GETUPVAL A=callA B=upvalue 索引
//	  [1] TAILCALL A=callA B=1 C=0
//	  [2] RETURN   A=callA B=0
//	  [3] RETURN   A=0 B=1
//
//	形态 TA1K/TB1K:1 K 参(长度 5)
//	  [0] MOVE/GETUPVAL A=callA B=...
//	  [1] LOADK    A=callA+1 Bx=K idx
//	  [2] TAILCALL A=callA B=2 C=0
//	  [3] RETURN   A=callA B=0
//	  [4] RETURN   A=0 B=1
//
//	形态 TA1R/TB1R:1 reg 参(长度 5)
//	  [0] MOVE/GETUPVAL A=callA B=...
//	  [1] MOVE     A=callA+1 B=源 reg
//	  [2] TAILCALL A=callA B=2 C=0
//	  [3] RETURN   A=callA B=0
//	  [4] RETURN   A=0 B=1
//
//	形态 TA2K/TB2K:2 K 参(长度 6)
//	  [0] MOVE/GETUPVAL A=callA
//	  [1] LOADK    A=callA+1
//	  [2] LOADK    A=callA+2
//	  [3] TAILCALL A=callA B=3 C=0
//	  [4] RETURN   A=callA B=0
//	  [5] RETURN   A=0 B=1
//
// **Run 端 prelude 路径**(参 code.go::Run TAILCALL case):
//   - MOVE/GETUPVAL 装 callee 到 R(callA)(host.GetReg/GetUpval + SetReg)
//   - LOADK/MOVE 参装 R(callA+1)/R(callA+2)
//   - 调 host.TailCall(base, tailPC, callA, callB, callC) 三态分支:
//     0=Lua 尾完成 → Run 直接 return 0(跳过 DoReturn,本帧已弹)
//     1=ERR → return 1
//     2=host 尾完成 → Run 落 dead RETURN to-top 走 DoReturn(retB=0 to-top)
//
// 失败任一条件返 (shapeInfo{}, false) — 走 analyzeCallVoidForm 路径(CALL
// 形态)或更后续 analyzeShape 主分流。
func analyzeTailCallForm(proto *bytecode.Proto) (shapeInfo, bool) {
	codeLen := len(proto.Code)
	// TAILCALL 形态最短长度 4(0 参 + dead RETURN + 隐式 RETURN),最长 11(7 参)
	if codeLen < 4 || codeLen > 11 {
		return shapeInfo{}, false
	}
	op0 := bytecode.Op(proto.Code[0])
	if op0 != bytecode.MOVE && op0 != bytecode.GETUPVAL {
		return shapeInfo{}, false
	}
	op0A := bytecode.A(proto.Code[0])
	op0B := bytecode.B(proto.Code[0])
	if op0A > 254 || op0B > 254 {
		return shapeInfo{}, false
	}

	var tailIdx int
	var argK uint64
	var argReg uint8
	var arg2K uint64
	var arg2Reg uint8
	var arg2IsK bool
	var arg3K uint64
	var arg3Reg uint8
	var arg3IsK bool
	var arg4K uint64
	var arg4Reg uint8
	var arg4IsK bool
	var arg5K uint64
	var arg5Reg uint8
	var arg5IsK bool
	var arg6K uint64
	var arg6Reg uint8
	var arg6IsK bool
	var arg7K uint64
	var arg7Reg uint8
	var arg7IsK bool
	var argCount uint8
	var argIsK bool
	switch codeLen {
	case 4:
		// 0 参:[0] MOVE/GETUPVAL,[1] TAILCALL,[2] RETURN B=0,[3] RETURN B=1
		tailIdx = 1
		argCount = 0
	case 5:
		// 1 参:[0] MOVE/GETUPVAL,[1] LOADK/MOVE,[2] TAILCALL,[3] RETURN B=0,
		// [4] RETURN B=1
		secondOp := bytecode.Op(proto.Code[1])
		switch secondOp {
		case bytecode.LOADK:
			lkA := bytecode.A(proto.Code[1])
			lkBx := bytecode.Bx(proto.Code[1])
			if lkA != op0A+1 {
				return shapeInfo{}, false
			}
			if lkBx < 0 || lkBx >= len(proto.Consts) {
				return shapeInfo{}, false
			}
			argK = uint64(proto.Consts[lkBx])
			argIsK = true
			argCount = 1
		case bytecode.MOVE:
			mvA := bytecode.A(proto.Code[1])
			mvB := bytecode.B(proto.Code[1])
			if mvA != op0A+1 {
				return shapeInfo{}, false
			}
			if mvB > 254 {
				return shapeInfo{}, false
			}
			argReg = uint8(mvB)
			argIsK = false
			argCount = 1
		default:
			return shapeInfo{}, false
		}
		tailIdx = 2
	case 6:
		// 2 参:[0] MOVE/GETUPVAL,[1] (LOADK|MOVE),[2] (LOADK|MOVE),[3] TAILCALL,
		// [4] RETURN B=0,[5] RETURN B=1 — 四组合 K+K / K+R / R+K / R+R
		secondOp := bytecode.Op(proto.Code[1])
		thirdOp := bytecode.Op(proto.Code[2])
		if (secondOp != bytecode.LOADK && secondOp != bytecode.MOVE) ||
			(thirdOp != bytecode.LOADK && thirdOp != bytecode.MOVE) {
			return shapeInfo{}, false
		}
		op2A := bytecode.A(proto.Code[1])
		op3A := bytecode.A(proto.Code[2])
		if op2A != op0A+1 || op3A != op0A+2 {
			return shapeInfo{}, false
		}
		// 第一参装载
		if secondOp == bytecode.LOADK {
			lk1Bx := bytecode.Bx(proto.Code[1])
			if lk1Bx < 0 || lk1Bx >= len(proto.Consts) {
				return shapeInfo{}, false
			}
			argK = uint64(proto.Consts[lk1Bx])
			argIsK = true
		} else {
			mv1B := bytecode.B(proto.Code[1])
			if mv1B > 254 {
				return shapeInfo{}, false
			}
			argReg = uint8(mv1B)
			argIsK = false
		}
		// 第二参装载
		if thirdOp == bytecode.LOADK {
			lk2Bx := bytecode.Bx(proto.Code[2])
			if lk2Bx < 0 || lk2Bx >= len(proto.Consts) {
				return shapeInfo{}, false
			}
			arg2K = uint64(proto.Consts[lk2Bx])
			arg2IsK = true
		} else {
			mv2B := bytecode.B(proto.Code[2])
			if mv2B > 254 {
				return shapeInfo{}, false
			}
			arg2Reg = uint8(mv2B)
			arg2IsK = false
		}
		argCount = 2
		tailIdx = 3
	case 7:
		// 3 参:[0] MOVE/GETUPVAL,[1..3] (LOADK|MOVE),[4] TAILCALL,
		// [5] RETURN B=0,[6] RETURN B=1 — 四组合 K+K+K / K+K+R / ... / R+R+R 8 子
		if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) ||
			!decodeArgFromOp(proto, 2, op0A+2, &arg2IsK, &arg2K, &arg2Reg) ||
			!decodeArgFromOp(proto, 3, op0A+3, &arg3IsK, &arg3K, &arg3Reg) {
			return shapeInfo{}, false
		}
		argCount = 3
		tailIdx = 4
	case 8:
		// 4 参:[0] MOVE/GETUPVAL,[1..4] (LOADK|MOVE),[5] TAILCALL,
		// [6] RETURN B=0,[7] RETURN B=1 — 四组合扩到 16 个但本批仅支持
		// 全 K / 全 reg 混合(decodeArgFromOp 接受任何 LOADK/MOVE 组合)
		if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) ||
			!decodeArgFromOp(proto, 2, op0A+2, &arg2IsK, &arg2K, &arg2Reg) ||
			!decodeArgFromOp(proto, 3, op0A+3, &arg3IsK, &arg3K, &arg3Reg) ||
			!decodeArgFromOp(proto, 4, op0A+4, &arg4IsK, &arg4K, &arg4Reg) {
			return shapeInfo{}, false
		}
		argCount = 4
		tailIdx = 5
	case 9:
		// 5 参:[0] MOVE/GETUPVAL,[1..5] (LOADK|MOVE),[6] TAILCALL,
		// [7] RETURN B=0,[8] RETURN B=1
		if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) ||
			!decodeArgFromOp(proto, 2, op0A+2, &arg2IsK, &arg2K, &arg2Reg) ||
			!decodeArgFromOp(proto, 3, op0A+3, &arg3IsK, &arg3K, &arg3Reg) ||
			!decodeArgFromOp(proto, 4, op0A+4, &arg4IsK, &arg4K, &arg4Reg) ||
			!decodeArgFromOp(proto, 5, op0A+5, &arg5IsK, &arg5K, &arg5Reg) {
			return shapeInfo{}, false
		}
		argCount = 5
		tailIdx = 6
	case 10:
		// 6 参 TAILCALL:[0] MOVE/GETUPVAL,[1..6] (LOADK|MOVE),[7] TAILCALL,
		// [8] RETURN B=0,[9] RETURN B=1
		if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) ||
			!decodeArgFromOp(proto, 2, op0A+2, &arg2IsK, &arg2K, &arg2Reg) ||
			!decodeArgFromOp(proto, 3, op0A+3, &arg3IsK, &arg3K, &arg3Reg) ||
			!decodeArgFromOp(proto, 4, op0A+4, &arg4IsK, &arg4K, &arg4Reg) ||
			!decodeArgFromOp(proto, 5, op0A+5, &arg5IsK, &arg5K, &arg5Reg) ||
			!decodeArgFromOp(proto, 6, op0A+6, &arg6IsK, &arg6K, &arg6Reg) {
			return shapeInfo{}, false
		}
		argCount = 6
		tailIdx = 7
	case 11:
		// 7 参 TAILCALL:[0] MOVE/GETUPVAL,[1..7] (LOADK|MOVE),[8] TAILCALL,
		// [9] RETURN B=0,[10] RETURN B=1
		if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) ||
			!decodeArgFromOp(proto, 2, op0A+2, &arg2IsK, &arg2K, &arg2Reg) ||
			!decodeArgFromOp(proto, 3, op0A+3, &arg3IsK, &arg3K, &arg3Reg) ||
			!decodeArgFromOp(proto, 4, op0A+4, &arg4IsK, &arg4K, &arg4Reg) ||
			!decodeArgFromOp(proto, 5, op0A+5, &arg5IsK, &arg5K, &arg5Reg) ||
			!decodeArgFromOp(proto, 6, op0A+6, &arg6IsK, &arg6K, &arg6Reg) ||
			!decodeArgFromOp(proto, 7, op0A+7, &arg7IsK, &arg7K, &arg7Reg) {
			return shapeInfo{}, false
		}
		argCount = 7
		tailIdx = 8
	}

	// 校验 TAILCALL + dead RETURN B=0 + 隐式 RETURN B=1 的尾部三元
	if bytecode.Op(proto.Code[tailIdx]) != bytecode.TAILCALL {
		return shapeInfo{}, false
	}
	deadRet := proto.Code[tailIdx+1]
	implRet := proto.Code[tailIdx+2]
	if bytecode.Op(deadRet) != bytecode.RETURN || bytecode.B(deadRet) != 0 {
		return shapeInfo{}, false
	}
	if bytecode.Op(implRet) != bytecode.RETURN || bytecode.B(implRet) != 1 {
		return shapeInfo{}, false
	}
	tlA := bytecode.A(proto.Code[tailIdx])
	tlB := bytecode.B(proto.Code[tailIdx])
	tlC := bytecode.C(proto.Code[tailIdx])
	if tlA != op0A {
		return shapeInfo{}, false
	}
	// TAILCALL.B = argCount + 1;TAILCALL.C 恒 0
	if int(tlB) != int(argCount)+1 {
		return shapeInfo{}, false
	}
	if tlC != 0 {
		return shapeInfo{}, false
	}
	// dead RETURN.A 必须 = callA(returnRange 起 callA 到 top)
	if bytecode.A(deadRet) != tlA {
		return shapeInfo{}, false
	}

	// **Run 端契约**:host.TailCall 返 2(host 尾)时 Run 落 dead RETURN B=0
	// (to-top)走 DoReturn,故 retPC 指 dead RETURN、retA=callA、retB=0
	// (DoReturn 内 B=0 → nret = top - (base + a),复用解释器多值返回路径)。
	return shapeInfo{
		ok:             true,
		retA:           uint8(tlA),
		retB:           0, // dead RETURN B=0,DoReturn 多值 to-top 路径
		retPC:          uint8(tailIdx + 1),
		preludeOp:      uint8(bytecode.TAILCALL),
		preludeArg:     uint32(op0B),
		isTailCall:     true,
		isCallUpval:    op0 == bytecode.GETUPVAL,
		callA:          uint8(tlA),
		callB:          uint8(tlB),
		callC:          uint8(tlC),
		callArgCount:   argCount,
		callArg1IsK:    argIsK,
		callArg1K:      argK,
		callArg1RegSrc: argReg,
		callArg2IsK:    arg2IsK,
		callArg2K:      arg2K,
		callArg2RegSrc: arg2Reg,
		callArg3IsK:    arg3IsK,
		callArg3K:      arg3K,
		callArg3RegSrc: arg3Reg,
		callArg4IsK:    arg4IsK,
		callArg4K:      arg4K,
		callArg4RegSrc: arg4Reg,
		callArg5IsK:    arg5IsK,
		callArg5K:      arg5K,
		callArg5RegSrc: arg5Reg,
		callArg6IsK:    arg6IsK,
		callArg6K:      arg6K,
		callArg6RegSrc: arg6Reg,
		callArg7IsK:    arg7IsK,
		callArg7K:      arg7K,
		callArg7RegSrc: arg7Reg,
	}, true
}

// analyzeSelfCallForm 识别 PJ5 SELF method call inline 形态(承
// docs/design/p4-method-jit/05-system-pipeline.md §4.3 + 09 §9.17):
//
// `function(o) o:m() end` / `function() o:m() end`(upval recv)/
// `function(o) return o:m() end`(SELF + TAILCALL)等。luac 编 SELF + CALL/
// TAILCALL inline 形态长度 4..6(渐进白名单纪律,初批先做 0/1 K/1 reg/2 K
// 参 × 双 receiver(M/U)× CALL void / CALL getter 1 返 / TAILCALL)。
//
// **典型 luac 编译形态**(0 参 void,长度 4):
//
//	形态 M0:`function(o) o:m() end`(recv from parameter reg)
//	  [0] MOVE     A=callA B=recvSrc   (拷 recv 到 R(callA),供 SELF.B 读)
//	  [1] SELF     A=callA B=callA C=K_method (R(callA)=R(callA)[K_m]; R(callA+1)=R(callA))
//	  [2] CALL     A=callA B=2 C=1     (0 参 0 返)
//	  [3] RETURN   A=0     B=1
//
//	形态 U0:`function() o:m() end`(recv from upvalue,o 是 upval)
//	  [0] GETUPVAL A=callA B=upvalIdx
//	  [1] SELF     A=callA B=callA C=K_m
//	  [2] CALL     A=callA B=2 C=1
//	  [3] RETURN   A=0     B=1
//
// **触发条件**(共同):
//   - Code 长度 4..6
//   - [0] = MOVE 或 GETUPVAL,[0].A == [1].A == [1].B(SELF/CALL 链 callA 一致)
//   - [1] = SELF,A=callA B=callA C>=256(K 常量索引)
//
// 失败任一条件返 (shapeInfo{}, false)— 走原 analyzeShape 主分流。
func analyzeSelfCallForm(proto *bytecode.Proto) (shapeInfo, bool) {
	codeLen := len(proto.Code)
	if codeLen < 4 || codeLen > 11 {
		return shapeInfo{}, false
	}
	op0 := bytecode.Op(proto.Code[0])
	if op0 != bytecode.MOVE && op0 != bytecode.GETUPVAL {
		return shapeInfo{}, false
	}
	op0A := bytecode.A(proto.Code[0])
	op0B := bytecode.B(proto.Code[0])
	if op0A > 254 || op0B > 254 {
		return shapeInfo{}, false
	}
	// [1] = SELF callA callA RK_method
	op1 := bytecode.Op(proto.Code[1])
	if op1 != bytecode.SELF {
		return shapeInfo{}, false
	}
	selfA := bytecode.A(proto.Code[1])
	selfB := bytecode.B(proto.Code[1])
	selfC := bytecode.C(proto.Code[1])
	if selfA != op0A || selfB != op0A {
		// SELF 的 A/B 必须 = MOVE/GETUPVAL.A(因为前置 op 装 recv 到 R(callA))
		return shapeInfo{}, false
	}
	if selfC < 256 {
		// SELF.C 必须是 K 常量索引(method 名)— reg 形态留 PJ5+ 扩
		return shapeInfo{}, false
	}
	callA := uint8(selfA)
	selfRK := uint16(selfC)

	switch codeLen {
	case 4:
		return analyzeSelfCallForm4(proto, callA, selfRK, op0, op0B)
	case 5:
		return analyzeSelfCallForm5(proto, callA, selfRK, op0, op0B)
	case 6:
		return analyzeSelfCallForm6(proto, callA, selfRK, op0, op0B)
	case 7:
		return analyzeSelfCallForm7(proto, callA, selfRK, op0, op0B)
	case 8:
		return analyzeSelfCallForm8(proto, callA, selfRK, op0, op0B)
	case 9:
		return analyzeSelfCallForm9(proto, callA, selfRK, op0, op0B)
	case 10:
		return analyzeSelfCallFormN(proto, callA, selfRK, op0, op0B, 6)
	case 11:
		return analyzeSelfCallFormN(proto, callA, selfRK, op0, op0B, 7)
	}
	return shapeInfo{}, false
}

// analyzeSelfCallFormN 处理长度 (4 + N args) 形态:CALL void N 参 / TAILCALL (N-1) 参。
// callOpIdx = 2 + N(CALL 在 N 条 LOADK/MOVE 之后)。
//
// 长度 10:N=6,CALL void 6 参 / TAILCALL 5 参(共已在 form9 处理 5 参 TAILCALL,本 form 仅做 CALL void 6 参 / TAILCALL 5 参 共存识别)
// 长度 11:N=7,CALL void 7 参 / TAILCALL 6 参
//
// 简化策略:本函数只识别 CALL void N 参 + TAILCALL (N-1) 参 两类。
func analyzeSelfCallFormN(proto *bytecode.Proto, callA uint8, selfRK uint16,
	op0 bytecode.OpCode, op0B int, nArgs int) (shapeInfo, bool) {
	// CALL void N 参:[2..1+N] LOADK/MOVE,[2+N] CALL B=N+2 C=1,[3+N] RETURN B=1
	callOpIdx := 2 + nArgs
	if bytecode.Op(proto.Code[callOpIdx]) == bytecode.CALL {
		argsIsK := make([]bool, nArgs)
		argsK := make([]uint64, nArgs)
		argsReg := make([]uint8, nArgs)
		for i := 0; i < nArgs; i++ {
			if !decodeArgFromOp(proto, 2+i, int(callA)+2+i, &argsIsK[i], &argsK[i], &argsReg[i]) {
				return shapeInfo{}, false
			}
		}
		cA := bytecode.A(proto.Code[callOpIdx])
		cB := bytecode.B(proto.Code[callOpIdx])
		cC := bytecode.C(proto.Code[callOpIdx])
		// cC=1 void(0 返)/ cC=3,4 N=2,3 返 drop multi-ret(`local a,b=t:m(K×N)`类)
		if cA != int(callA) || cB != nArgs+2 || (cC != 1 && cC != 3 && cC != 4) {
			return shapeInfo{}, false
		}
		if bytecode.Op(proto.Code[callOpIdx+1]) != bytecode.RETURN ||
			bytecode.B(proto.Code[callOpIdx+1]) != 1 {
			return shapeInfo{}, false
		}
		info := shapeInfo{
			ok:              true,
			retA:            0,
			retB:            1,
			retPC:           uint8(callOpIdx + 1),
			preludeOp:       uint8(bytecode.CALL),
			preludeArg:      uint32(op0B),
			isCallVoid:      true,
			isCallUpval:     op0 == bytecode.GETUPVAL,
			callA:           callA,
			callB:           uint8(cB),
			callC:           uint8(cC),
			callArgCount:    uint8(nArgs),
			isSelfCall:      true,
			selfCallA:       callA,
			selfMethodRK:    selfRK,
			selfRecvSrcReg:  uint8(op0B),
			selfRecvIsUpval: op0 == bytecode.GETUPVAL,
		}
		assignArgsToShape(&info, argsIsK, argsK, argsReg)
		return info, true
	}
	// TAILCALL (N-1) 参:实际 [callOpIdx-1] TAILCALL,[callOpIdx] RETURN A=callA B=0,
	// [callOpIdx+1] RETURN B=1。即长度 10 N=6 实际 TAILCALL 5 参(callOpIdx-1 = 7)。
	// 简化:外层 codeLen 推 nArgs (= codeLen - 4)只识别 N 参 CALL 形态;
	// 多余 TAILCALL 形态留 form7/8/9 处理(它们已支持长度 7/8/9 = 2/3/4 参 TAILCALL)。
	// 长度 10:TAILCALL 5 参 — 走 form10 的 callOpIdx-1 探测:
	tailOpIdx := callOpIdx - 1 // 6 args - 1 = 5 args TAILCALL,callOpIdx-1 处
	if bytecode.Op(proto.Code[tailOpIdx]) == bytecode.TAILCALL {
		nTailArgs := nArgs - 1
		argsIsK := make([]bool, nTailArgs)
		argsK := make([]uint64, nTailArgs)
		argsReg := make([]uint8, nTailArgs)
		for i := 0; i < nTailArgs; i++ {
			if !decodeArgFromOp(proto, 2+i, int(callA)+2+i, &argsIsK[i], &argsK[i], &argsReg[i]) {
				return shapeInfo{}, false
			}
		}
		cA := bytecode.A(proto.Code[tailOpIdx])
		cB := bytecode.B(proto.Code[tailOpIdx])
		cC := bytecode.C(proto.Code[tailOpIdx])
		if cA != int(callA) || cB != nTailArgs+2 || cC != 0 {
			return shapeInfo{}, false
		}
		if bytecode.Op(proto.Code[tailOpIdx+1]) != bytecode.RETURN ||
			bytecode.A(proto.Code[tailOpIdx+1]) != int(callA) ||
			bytecode.B(proto.Code[tailOpIdx+1]) != 0 ||
			bytecode.Op(proto.Code[tailOpIdx+2]) != bytecode.RETURN ||
			bytecode.B(proto.Code[tailOpIdx+2]) != 1 {
			return shapeInfo{}, false
		}
		info := shapeInfo{
			ok:              true,
			retA:            uint8(bytecode.A(proto.Code[tailOpIdx+1])),
			retB:            0,
			retPC:           uint8(tailOpIdx + 1),
			preludeOp:       uint8(bytecode.TAILCALL),
			preludeArg:      uint32(op0B),
			isTailCall:      true,
			isCallUpval:     op0 == bytecode.GETUPVAL,
			callA:           callA,
			callB:           uint8(cB),
			callC:           uint8(cC),
			callArgCount:    uint8(nTailArgs),
			isSelfCall:      true,
			selfCallA:       callA,
			selfMethodRK:    selfRK,
			selfRecvSrcReg:  uint8(op0B),
			selfRecvIsUpval: op0 == bytecode.GETUPVAL,
		}
		assignArgsToShape(&info, argsIsK, argsK, argsReg)
		return info, true
	}
	return shapeInfo{}, false
}

// analyzeSelfCallSpecForm 识别 PJ5 SELF + CALL spec template 接入形态(承
// §9.10 PJ4 EmitSelfNodeHit 复用 + §9.17 PJ5 SELF inline 升级):
//
// **形态边界**(承 analyzeSelfCallForm 完整 0..7 参 void/getter/tail 形态,
// 叠加 IC NodeHit feedback 命中守门):
//
//	[0] MOVE/GETUPVAL A=callA B=recvSrc  (装 recv 到 R(callA))
//	[1] SELF     A=callA B=callA C=K_method  (IC[1] = NodeHit feedback)
//	[2..1+N] LOADK/MOVE args(args 装到 R(callA+2..callA+1+N))
//	[2+N] CALL/TAILCALL A=callA B=N+2 C=1/0/2 (N 参 0/0返/1 返)
//	[3+N] RETURN ...
//
// **触发条件**:
//   - analyzeSelfCallForm(proto) 返回普通 SELF inline shapeInfo(任意 0..7 参形态)
//   - proto.IC[1].Kind == ICKindNodeHit(P1 解释器观测过 hash 段命中)
//   - feedback.Points[1].Kind == FBSelfMono + Confidence >= 0.99
//   - stableShape/Index 一致 + stableKey 编译期固化 != Nil
//
// 失败任一条件返 (shapeInfo{}, false) — 走 analyzeSelfCallForm 普通(host.Self)路径。
//
// **Run 端执行**:runSpecSelfCall(spec 段跑 EmitSelfNodeHit 跳过 host.Self,
// 失败 deopt 降级 host.Self)+ 装 args + host.CallBaseline + host.DoReturn。
func analyzeSelfCallSpecForm(proto *bytecode.Proto, feedback *bridge.TypeFeedback) (shapeInfo, bool) {
	// 先识别普通 SELF inline 形态(任意 0..7 参 void/getter/tail)
	info, ok := analyzeSelfCallForm(proto)
	if !ok {
		return shapeInfo{}, false
	}
	// spec template 启用范围(承 isCallVoid 实际语义 = preludeOp=CALL,涵盖
	// 多形态):CALL void retB=1 setter / CALL getter retB=2 1 返 / CALL retB=1
	// + cC=3/4 N>=2 返(local a,b=t:m()类 drop multi-ret)+ TAILCALL 三态分支。
	// host.CallBaseline 按 callC 把返值落 R(callA..callA+nret-1),host.DoReturn
	// 按 retA/retB 走主调 RETURN 语义,两层协议解耦,spec 段统一只负责 SELF/args/recv。
	if !info.isCallVoid && !info.isTailCall {
		return shapeInfo{}, false
	}
	// IC slot 检查(SELF 在 pc=1,故 proto.IC[1])
	if len(proto.IC) <= 1 {
		return shapeInfo{}, false
	}
	icSlot := proto.IC[1]
	if icSlot.Kind != bytecode.ICKindNodeHit {
		return shapeInfo{}, false
	}
	// feedback 检查(Points[1] 对位 SELF pc=1)。**SELF 聚合成 FBSelfMono**
	// (aggregator.go::extractTableFeedback opSelf 分支),非 FBTableMono——
	// PJ5 SELF + CALL 是首个真实触达 SELF feedback 的路径(PJ4 SELF NodeHit
	// 因 luac 不真编 SELF + RETURN 2-op 形态仅合成驱动单测,从未触达真 SELF
	// feedback,故那里误用 FBTableMono;本路径用正确的 FBSelfMono)。
	if feedback == nil || len(feedback.Points) < 2 {
		return shapeInfo{}, false
	}
	pf := feedback.Points[1]
	if pf.Kind != bridge.FBSelfMono || pf.Confidence < 0.99 {
		return shapeInfo{}, false
	}
	if pf.StableShape != icSlot.Shape || pf.StableIndex != icSlot.Index {
		return shapeInfo{}, false
	}
	// stableKey 编译期固化(SELF.C 是 K 常量索引)
	selfC := bytecode.C(proto.Code[1])
	kIdx := bytecode.KIdx(selfC)
	if kIdx < 0 || kIdx >= len(proto.Consts) {
		return shapeInfo{}, false
	}
	stableKey := uint64(proto.Consts[kIdx])
	if stableKey == uint64(value.Nil) {
		return shapeInfo{}, false
	}
	// 叠加 spec template 字段(保留 analyzeSelfCallForm 的所有形态字段:
	// callArgCount / callArg1..7K/RegSrc / isCallVoid / isCallUpval 等)
	info.useSpecSelfCall = true
	info.icAReg = info.callA
	info.icBReg = info.callA // SELF.B = callA(recv 槽,MOVE/GETUPVAL 装)
	info.icStableShape = pf.StableShape
	info.icStableIndex = pf.StableIndex
	info.icStableKey = stableKey
	return info, true
}

// assignArgsToShape 把 args 数组(N=2..7)对应字段填到 shapeInfo。
func assignArgsToShape(info *shapeInfo, argsIsK []bool, argsK []uint64, argsReg []uint8) {
	n := len(argsIsK)
	if n >= 1 {
		info.callArg1IsK = argsIsK[0]
		info.callArg1K = argsK[0]
		info.callArg1RegSrc = argsReg[0]
	}
	if n >= 2 {
		info.callArg2IsK = argsIsK[1]
		info.callArg2K = argsK[1]
		info.callArg2RegSrc = argsReg[1]
	}
	if n >= 3 {
		info.callArg3IsK = argsIsK[2]
		info.callArg3K = argsK[2]
		info.callArg3RegSrc = argsReg[2]
	}
	if n >= 4 {
		info.callArg4IsK = argsIsK[3]
		info.callArg4K = argsK[3]
		info.callArg4RegSrc = argsReg[3]
	}
	if n >= 5 {
		info.callArg5IsK = argsIsK[4]
		info.callArg5K = argsK[4]
		info.callArg5RegSrc = argsReg[4]
	}
	if n >= 6 {
		info.callArg6IsK = argsIsK[5]
		info.callArg6K = argsK[5]
		info.callArg6RegSrc = argsReg[5]
	}
	if n >= 7 {
		info.callArg7IsK = argsIsK[6]
		info.callArg7K = argsK[6]
		info.callArg7RegSrc = argsReg[6]
	}
}

// analyzeSelfCallForm4 处理长度 4 形态:0 参 0 返 CALL void / 0 参 TAILCALL。
func analyzeSelfCallForm4(proto *bytecode.Proto, callA uint8, selfRK uint16,
	op0 bytecode.OpCode, op0B int) (shapeInfo, bool) {
	op2 := bytecode.Op(proto.Code[2])
	op3 := bytecode.Op(proto.Code[3])
	if op2 != bytecode.CALL && op2 != bytecode.TAILCALL {
		return shapeInfo{}, false
	}
	if op3 != bytecode.RETURN {
		return shapeInfo{}, false
	}
	cA := bytecode.A(proto.Code[2])
	cB := bytecode.B(proto.Code[2])
	cC := bytecode.C(proto.Code[2])
	if cA != int(callA) || cB != 2 {
		return shapeInfo{}, false
	}
	if op2 == bytecode.CALL {
		if cC == 1 {
			if bytecode.B(proto.Code[3]) != 1 {
				return shapeInfo{}, false
			}
			return shapeInfo{
				ok:              true,
				retA:            0,
				retB:            1,
				retPC:           3,
				preludeOp:       uint8(bytecode.CALL),
				preludeArg:      uint32(op0B),
				isCallVoid:      true,
				isCallUpval:     op0 == bytecode.GETUPVAL,
				callA:           callA,
				callB:           uint8(cB),
				callC:           uint8(cC),
				callArgCount:    0,
				isSelfCall:      true,
				selfCallA:       callA,
				selfMethodRK:    selfRK,
				selfRecvSrcReg:  uint8(op0B),
				selfRecvIsUpval: op0 == bytecode.GETUPVAL,
			}, true
		}
		// N>=2 返值 getter:cC=3(N=2)/ cC=4(N=3),`local a,b = o:m()` 类
		// luac 编 [3]=RETURN B=1(主 chunk 隐式 RETURN 收尾,N>=2 返值已落
		// R(callA..callA+nret-1)作 local 直接绑;P4 帧不返这些 local 出去,
		// 所以 retB=1 是正确的 0 返值收尾)
		if cC == 3 || cC == 4 {
			if bytecode.B(proto.Code[3]) != 1 {
				return shapeInfo{}, false
			}
			return shapeInfo{
				ok:              true,
				retA:            0,
				retB:            1,
				retPC:           3,
				preludeOp:       uint8(bytecode.CALL),
				preludeArg:      uint32(op0B),
				isCallVoid:      true,
				isCallUpval:     op0 == bytecode.GETUPVAL,
				callA:           callA,
				callB:           uint8(cB),
				callC:           uint8(cC),
				callArgCount:    0,
				isSelfCall:      true,
				selfCallA:       callA,
				selfMethodRK:    selfRK,
				selfRecvSrcReg:  uint8(op0B),
				selfRecvIsUpval: op0 == bytecode.GETUPVAL,
			}, true
		}
		return shapeInfo{}, false
	}
	// TAILCALL
	if cC != 0 {
		return shapeInfo{}, false
	}
	retB := bytecode.B(proto.Code[3])
	if retB != 0 {
		return shapeInfo{}, false
	}
	return shapeInfo{
		ok:              true,
		retA:            uint8(bytecode.A(proto.Code[3])),
		retB:            uint8(retB),
		retPC:           3,
		preludeOp:       uint8(bytecode.TAILCALL),
		preludeArg:      uint32(op0B),
		isTailCall:      true,
		isCallUpval:     op0 == bytecode.GETUPVAL,
		callA:           callA,
		callB:           uint8(cB),
		callC:           uint8(cC),
		callArgCount:    0,
		isSelfCall:      true,
		selfCallA:       callA,
		selfMethodRK:    selfRK,
		selfRecvSrcReg:  uint8(op0B),
		selfRecvIsUpval: op0 == bytecode.GETUPVAL,
	}, true
}

// analyzeSelfCallForm5 处理长度 5 形态:CALL getter 0 参 1 返 / CALL void 1 K/reg 参 / TAILCALL 1 K/reg 参。
func analyzeSelfCallForm5(proto *bytecode.Proto, callA uint8, selfRK uint16,
	op0 bytecode.OpCode, op0B int) (shapeInfo, bool) {
	op2 := bytecode.Op(proto.Code[2])
	op3 := bytecode.Op(proto.Code[3])
	op4 := bytecode.Op(proto.Code[4])
	// (a) Code[2]=CALL → getter 0 参 1 返
	if op2 == bytecode.CALL {
		cA := bytecode.A(proto.Code[2])
		cB := bytecode.B(proto.Code[2])
		cC := bytecode.C(proto.Code[2])
		if cA != int(callA) || cB != 2 || cC != 2 {
			return shapeInfo{}, false
		}
		if op3 != bytecode.RETURN || op4 != bytecode.RETURN {
			return shapeInfo{}, false
		}
		rA := bytecode.A(proto.Code[3])
		rB := bytecode.B(proto.Code[3])
		if rA != int(callA) || rB != 2 {
			return shapeInfo{}, false
		}
		if bytecode.B(proto.Code[4]) != 1 {
			return shapeInfo{}, false
		}
		return shapeInfo{
			ok:              true,
			retA:            uint8(rA),
			retB:            2,
			retPC:           3,
			preludeOp:       uint8(bytecode.CALL),
			preludeArg:      uint32(op0B),
			isCallVoid:      true,
			isCallUpval:     op0 == bytecode.GETUPVAL,
			callA:           callA,
			callB:           uint8(cB),
			callC:           uint8(cC),
			callArgCount:    0,
			isSelfCall:      true,
			selfCallA:       callA,
			selfMethodRK:    selfRK,
			selfRecvSrcReg:  uint8(op0B),
			selfRecvIsUpval: op0 == bytecode.GETUPVAL,
		}, true
	}
	// (a') Code[2]=TAILCALL 0 参:[2] TAILCALL B=2 C=0,[3] RETURN A=callA B=0(dead),[4] RETURN B=1(隐式)
	if op2 == bytecode.TAILCALL {
		cA := bytecode.A(proto.Code[2])
		cB := bytecode.B(proto.Code[2])
		cC := bytecode.C(proto.Code[2])
		if cA != int(callA) || cB != 2 || cC != 0 {
			return shapeInfo{}, false
		}
		if op3 != bytecode.RETURN || op4 != bytecode.RETURN {
			return shapeInfo{}, false
		}
		if bytecode.B(proto.Code[3]) != 0 || bytecode.A(proto.Code[3]) != int(callA) {
			return shapeInfo{}, false
		}
		if bytecode.B(proto.Code[4]) != 1 {
			return shapeInfo{}, false
		}
		return shapeInfo{
			ok:              true,
			retA:            uint8(bytecode.A(proto.Code[3])),
			retB:            0,
			retPC:           3,
			preludeOp:       uint8(bytecode.TAILCALL),
			preludeArg:      uint32(op0B),
			isTailCall:      true,
			isCallUpval:     op0 == bytecode.GETUPVAL,
			callA:           callA,
			callB:           uint8(cB),
			callC:           uint8(cC),
			callArgCount:    0,
			isSelfCall:      true,
			selfCallA:       callA,
			selfMethodRK:    selfRK,
			selfRecvSrcReg:  uint8(op0B),
			selfRecvIsUpval: op0 == bytecode.GETUPVAL,
		}, true
	}
	// (b)(c):[2] = LOADK/MOVE  arg → R(callA+2)
	if op2 != bytecode.LOADK && op2 != bytecode.MOVE {
		return shapeInfo{}, false
	}
	var argIsK bool
	var argK uint64
	var argReg uint8
	if !decodeArgFromOp(proto, 2, int(callA)+2, &argIsK, &argK, &argReg) {
		return shapeInfo{}, false
	}
	if op3 != bytecode.CALL && op3 != bytecode.TAILCALL {
		return shapeInfo{}, false
	}
	cA := bytecode.A(proto.Code[3])
	cB := bytecode.B(proto.Code[3])
	cC := bytecode.C(proto.Code[3])
	if cA != int(callA) || cB != 3 {
		return shapeInfo{}, false
	}
	if op4 != bytecode.RETURN {
		return shapeInfo{}, false
	}
	retB := bytecode.B(proto.Code[4])
	if op3 == bytecode.CALL {
		if cC == 1 && retB == 1 {
			return shapeInfo{
				ok:              true,
				retA:            0,
				retB:            1,
				retPC:           4,
				preludeOp:       uint8(bytecode.CALL),
				preludeArg:      uint32(op0B),
				isCallVoid:      true,
				isCallUpval:     op0 == bytecode.GETUPVAL,
				callA:           callA,
				callB:           uint8(cB),
				callC:           uint8(cC),
				callArgCount:    1,
				callArg1IsK:     argIsK,
				callArg1K:       argK,
				callArg1RegSrc:  argReg,
				isSelfCall:      true,
				selfCallA:       callA,
				selfMethodRK:    selfRK,
				selfRecvSrcReg:  uint8(op0B),
				selfRecvIsUpval: op0 == bytecode.GETUPVAL,
			}, true
		}
		// N>=2 返值 getter 1 K/reg 参:cC=3(N=2)/ cC=4(N=3),`local a,b = o:m(K/R)` 类
		if (cC == 3 || cC == 4) && retB == 1 {
			return shapeInfo{
				ok:              true,
				retA:            0,
				retB:            1,
				retPC:           4,
				preludeOp:       uint8(bytecode.CALL),
				preludeArg:      uint32(op0B),
				isCallVoid:      true,
				isCallUpval:     op0 == bytecode.GETUPVAL,
				callA:           callA,
				callB:           uint8(cB),
				callC:           uint8(cC),
				callArgCount:    1,
				callArg1IsK:     argIsK,
				callArg1K:       argK,
				callArg1RegSrc:  argReg,
				isSelfCall:      true,
				selfCallA:       callA,
				selfMethodRK:    selfRK,
				selfRecvSrcReg:  uint8(op0B),
				selfRecvIsUpval: op0 == bytecode.GETUPVAL,
			}, true
		}
		return shapeInfo{}, false
	}
	// TAILCALL 1 参
	if cC != 0 || retB != 0 {
		return shapeInfo{}, false
	}
	return shapeInfo{
		ok:              true,
		retA:            uint8(bytecode.A(proto.Code[4])),
		retB:            uint8(retB),
		retPC:           4,
		preludeOp:       uint8(bytecode.TAILCALL),
		preludeArg:      uint32(op0B),
		isTailCall:      true,
		isCallUpval:     op0 == bytecode.GETUPVAL,
		callA:           callA,
		callB:           uint8(cB),
		callC:           uint8(cC),
		callArgCount:    1,
		callArg1IsK:     argIsK,
		callArg1K:       argK,
		callArg1RegSrc:  argReg,
		isSelfCall:      true,
		selfCallA:       callA,
		selfMethodRK:    selfRK,
		selfRecvSrcReg:  uint8(op0B),
		selfRecvIsUpval: op0 == bytecode.GETUPVAL,
	}, true
}

// analyzeSelfCallForm6 处理长度 6 形态:CALL getter 1 K/reg 参 1 返 /
// CALL void 2 K/reg 参 / TAILCALL 2 K/reg 参。
func analyzeSelfCallForm6(proto *bytecode.Proto, callA uint8, selfRK uint16,
	op0 bytecode.OpCode, op0B int) (shapeInfo, bool) {
	op2 := bytecode.Op(proto.Code[2])
	if op2 != bytecode.LOADK && op2 != bytecode.MOVE {
		return shapeInfo{}, false
	}
	op3 := bytecode.Op(proto.Code[3])
	// (a) Code[3]=CALL → getter 1 参 1 返:[2] arg → R(callA+2),[3] CALL B=3 C=2,[4] RETURN A=callA B=2,[5] RETURN B=1
	if op3 == bytecode.CALL {
		var argIsK bool
		var argK uint64
		var argReg uint8
		if !decodeArgFromOp(proto, 2, int(callA)+2, &argIsK, &argK, &argReg) {
			return shapeInfo{}, false
		}
		cA := bytecode.A(proto.Code[3])
		cB := bytecode.B(proto.Code[3])
		cC := bytecode.C(proto.Code[3])
		if cA != int(callA) || cB != 3 || cC != 2 {
			return shapeInfo{}, false
		}
		if bytecode.Op(proto.Code[4]) != bytecode.RETURN ||
			bytecode.Op(proto.Code[5]) != bytecode.RETURN {
			return shapeInfo{}, false
		}
		rA := bytecode.A(proto.Code[4])
		rB := bytecode.B(proto.Code[4])
		if rA != int(callA) || rB != 2 || bytecode.B(proto.Code[5]) != 1 {
			return shapeInfo{}, false
		}
		return shapeInfo{
			ok:              true,
			retA:            uint8(rA),
			retB:            2,
			retPC:           4,
			preludeOp:       uint8(bytecode.CALL),
			preludeArg:      uint32(op0B),
			isCallVoid:      true,
			isCallUpval:     op0 == bytecode.GETUPVAL,
			callA:           callA,
			callB:           uint8(cB),
			callC:           uint8(cC),
			callArgCount:    1,
			callArg1IsK:     argIsK,
			callArg1K:       argK,
			callArg1RegSrc:  argReg,
			isSelfCall:      true,
			selfCallA:       callA,
			selfMethodRK:    selfRK,
			selfRecvSrcReg:  uint8(op0B),
			selfRecvIsUpval: op0 == bytecode.GETUPVAL,
		}, true
	}
	// (a') Code[3]=TAILCALL 1 参:[2] arg → R(callA+2),[3] TAILCALL B=3 C=0,
	// [4] RETURN A=callA B=0(dead),[5] RETURN B=1(隐式)
	if op3 == bytecode.TAILCALL {
		var argIsK bool
		var argK uint64
		var argReg uint8
		if !decodeArgFromOp(proto, 2, int(callA)+2, &argIsK, &argK, &argReg) {
			return shapeInfo{}, false
		}
		cA := bytecode.A(proto.Code[3])
		cB := bytecode.B(proto.Code[3])
		cC := bytecode.C(proto.Code[3])
		if cA != int(callA) || cB != 3 || cC != 0 {
			return shapeInfo{}, false
		}
		if bytecode.Op(proto.Code[4]) != bytecode.RETURN ||
			bytecode.Op(proto.Code[5]) != bytecode.RETURN {
			return shapeInfo{}, false
		}
		if bytecode.A(proto.Code[4]) != int(callA) ||
			bytecode.B(proto.Code[4]) != 0 ||
			bytecode.B(proto.Code[5]) != 1 {
			return shapeInfo{}, false
		}
		return shapeInfo{
			ok:              true,
			retA:            uint8(bytecode.A(proto.Code[4])),
			retB:            0,
			retPC:           4,
			preludeOp:       uint8(bytecode.TAILCALL),
			preludeArg:      uint32(op0B),
			isTailCall:      true,
			isCallUpval:     op0 == bytecode.GETUPVAL,
			callA:           callA,
			callB:           uint8(cB),
			callC:           uint8(cC),
			callArgCount:    1,
			callArg1IsK:     argIsK,
			callArg1K:       argK,
			callArg1RegSrc:  argReg,
			isSelfCall:      true,
			selfCallA:       callA,
			selfMethodRK:    selfRK,
			selfRecvSrcReg:  uint8(op0B),
			selfRecvIsUpval: op0 == bytecode.GETUPVAL,
		}, true
	}
	// (b)(c):2 K/reg 参 — [2][3] LOADK/MOVE
	if op3 != bytecode.LOADK && op3 != bytecode.MOVE {
		return shapeInfo{}, false
	}
	var arg1IsK bool
	var arg1K uint64
	var arg1Reg uint8
	var arg2IsK bool
	var arg2K uint64
	var arg2Reg uint8
	if !decodeArgFromOp(proto, 2, int(callA)+2, &arg1IsK, &arg1K, &arg1Reg) {
		return shapeInfo{}, false
	}
	if !decodeArgFromOp(proto, 3, int(callA)+3, &arg2IsK, &arg2K, &arg2Reg) {
		return shapeInfo{}, false
	}
	op4 := bytecode.Op(proto.Code[4])
	if op4 != bytecode.CALL && op4 != bytecode.TAILCALL {
		return shapeInfo{}, false
	}
	cA := bytecode.A(proto.Code[4])
	cB := bytecode.B(proto.Code[4])
	cC := bytecode.C(proto.Code[4])
	if cA != int(callA) || cB != 4 {
		return shapeInfo{}, false
	}
	if bytecode.Op(proto.Code[5]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	retB := bytecode.B(proto.Code[5])
	if op4 == bytecode.CALL {
		// cC=1 void(0 返)/ cC=3,4 N=2,3 返 drop multi-ret 形态(`local a,b=t:m(K,R)` 类)
		if (cC != 1 && cC != 3 && cC != 4) || retB != 1 {
			return shapeInfo{}, false
		}
		return shapeInfo{
			ok:              true,
			retA:            0,
			retB:            1,
			retPC:           5,
			preludeOp:       uint8(bytecode.CALL),
			preludeArg:      uint32(op0B),
			isCallVoid:      true,
			isCallUpval:     op0 == bytecode.GETUPVAL,
			callA:           callA,
			callB:           uint8(cB),
			callC:           uint8(cC),
			callArgCount:    2,
			callArg1IsK:     arg1IsK,
			callArg1K:       arg1K,
			callArg1RegSrc:  arg1Reg,
			callArg2IsK:     arg2IsK,
			callArg2K:       arg2K,
			callArg2RegSrc:  arg2Reg,
			isSelfCall:      true,
			selfCallA:       callA,
			selfMethodRK:    selfRK,
			selfRecvSrcReg:  uint8(op0B),
			selfRecvIsUpval: op0 == bytecode.GETUPVAL,
		}, true
	}
	// TAILCALL 2 参
	if cC != 0 || retB != 0 {
		return shapeInfo{}, false
	}
	return shapeInfo{
		ok:              true,
		retA:            uint8(bytecode.A(proto.Code[5])),
		retB:            uint8(retB),
		retPC:           5,
		preludeOp:       uint8(bytecode.TAILCALL),
		preludeArg:      uint32(op0B),
		isTailCall:      true,
		isCallUpval:     op0 == bytecode.GETUPVAL,
		callA:           callA,
		callB:           uint8(cB),
		callC:           uint8(cC),
		callArgCount:    2,
		callArg1IsK:     arg1IsK,
		callArg1K:       arg1K,
		callArg1RegSrc:  arg1Reg,
		callArg2IsK:     arg2IsK,
		callArg2K:       arg2K,
		callArg2RegSrc:  arg2Reg,
		isSelfCall:      true,
		selfCallA:       callA,
		selfMethodRK:    selfRK,
		selfRecvSrcReg:  uint8(op0B),
		selfRecvIsUpval: op0 == bytecode.GETUPVAL,
	}, true
}

// analyzeSelfCallForm7 处理长度 7 形态(共同 callee SELF + 3 op):
//   - CALL void 3 参:[2..4] LOADK/MOVE,[5] CALL B=5 C=1,[6] RETURN B=1
//   - CALL getter 1 返 2 参:[2..3] LOADK/MOVE,[4] CALL B=4 C=2,[5] RETURN A=callA B=2,[6] RETURN B=1
//   - CALL N=2/3 返 3 参 drop multi-ret:[2..4] LOADK/MOVE,[5] CALL B=5 C=3/4,[6] RETURN B=1
//   - TAILCALL 2 参:[2..3] LOADK/MOVE,[4] TAILCALL B=4 C=0,[5] RETURN A=callA B=0,[6] RETURN B=1
func analyzeSelfCallForm7(proto *bytecode.Proto, callA uint8, selfRK uint16,
	op0 bytecode.OpCode, op0B int) (shapeInfo, bool) {
	// 区分键:Code[4] = CALL → getter/N返 / Code[4] = TAILCALL → tail / Code[5] = CALL → void 3 参
	op4 := bytecode.Op(proto.Code[4])
	op5 := bytecode.Op(proto.Code[5])

	if op4 == bytecode.CALL || op4 == bytecode.TAILCALL {
		// 2 参 form(getter 1 返 / TAILCALL):[2][3] = LOADK/MOVE,[4] = CALL/TAILCALL,[5] = RETURN
		if bytecode.Op(proto.Code[2]) != bytecode.LOADK && bytecode.Op(proto.Code[2]) != bytecode.MOVE {
			return shapeInfo{}, false
		}
		if bytecode.Op(proto.Code[3]) != bytecode.LOADK && bytecode.Op(proto.Code[3]) != bytecode.MOVE {
			return shapeInfo{}, false
		}
		var arg1IsK, arg2IsK bool
		var arg1K, arg2K uint64
		var arg1Reg, arg2Reg uint8
		if !decodeArgFromOp(proto, 2, int(callA)+2, &arg1IsK, &arg1K, &arg1Reg) {
			return shapeInfo{}, false
		}
		if !decodeArgFromOp(proto, 3, int(callA)+3, &arg2IsK, &arg2K, &arg2Reg) {
			return shapeInfo{}, false
		}
		cA := bytecode.A(proto.Code[4])
		cB := bytecode.B(proto.Code[4])
		cC := bytecode.C(proto.Code[4])
		if cA != int(callA) || cB != 4 {
			return shapeInfo{}, false
		}
		if op4 == bytecode.CALL {
			// CALL getter 2 参 1 返:cC=2,[5] RETURN A=callA B=2,[6] RETURN B=1
			if cC != 2 {
				return shapeInfo{}, false
			}
			if bytecode.Op(proto.Code[5]) != bytecode.RETURN ||
				bytecode.Op(proto.Code[6]) != bytecode.RETURN {
				return shapeInfo{}, false
			}
			if bytecode.A(proto.Code[5]) != int(callA) ||
				bytecode.B(proto.Code[5]) != 2 ||
				bytecode.B(proto.Code[6]) != 1 {
				return shapeInfo{}, false
			}
			return shapeInfo{
				ok:              true,
				retA:            uint8(bytecode.A(proto.Code[5])),
				retB:            2,
				retPC:           5,
				preludeOp:       uint8(bytecode.CALL),
				preludeArg:      uint32(op0B),
				isCallVoid:      true,
				isCallUpval:     op0 == bytecode.GETUPVAL,
				callA:           callA,
				callB:           uint8(cB),
				callC:           uint8(cC),
				callArgCount:    2,
				callArg1IsK:     arg1IsK,
				callArg1K:       arg1K,
				callArg1RegSrc:  arg1Reg,
				callArg2IsK:     arg2IsK,
				callArg2K:       arg2K,
				callArg2RegSrc:  arg2Reg,
				isSelfCall:      true,
				selfCallA:       callA,
				selfMethodRK:    selfRK,
				selfRecvSrcReg:  uint8(op0B),
				selfRecvIsUpval: op0 == bytecode.GETUPVAL,
			}, true
		}
		// TAILCALL 2 参:cC=0,[5] RETURN A=callA B=0,[6] RETURN B=1
		if cC != 0 {
			return shapeInfo{}, false
		}
		if bytecode.Op(proto.Code[5]) != bytecode.RETURN ||
			bytecode.Op(proto.Code[6]) != bytecode.RETURN {
			return shapeInfo{}, false
		}
		if bytecode.A(proto.Code[5]) != int(callA) ||
			bytecode.B(proto.Code[5]) != 0 ||
			bytecode.B(proto.Code[6]) != 1 {
			return shapeInfo{}, false
		}
		return shapeInfo{
			ok:              true,
			retA:            uint8(bytecode.A(proto.Code[5])),
			retB:            0,
			retPC:           5,
			preludeOp:       uint8(bytecode.TAILCALL),
			preludeArg:      uint32(op0B),
			isTailCall:      true,
			isCallUpval:     op0 == bytecode.GETUPVAL,
			callA:           callA,
			callB:           uint8(cB),
			callC:           uint8(cC),
			callArgCount:    2,
			callArg1IsK:     arg1IsK,
			callArg1K:       arg1K,
			callArg1RegSrc:  arg1Reg,
			callArg2IsK:     arg2IsK,
			callArg2K:       arg2K,
			callArg2RegSrc:  arg2Reg,
			isSelfCall:      true,
			selfCallA:       callA,
			selfMethodRK:    selfRK,
			selfRecvSrcReg:  uint8(op0B),
			selfRecvIsUpval: op0 == bytecode.GETUPVAL,
		}, true
	}

	// Code[5] = CALL → CALL void 3 参:[2..4] LOADK/MOVE,[5] CALL B=5 C=1,[6] RETURN B=1
	if op5 == bytecode.CALL {
		var arg1IsK, arg2IsK, arg3IsK bool
		var arg1K, arg2K, arg3K uint64
		var arg1Reg, arg2Reg, arg3Reg uint8
		if !decodeArgFromOp(proto, 2, int(callA)+2, &arg1IsK, &arg1K, &arg1Reg) {
			return shapeInfo{}, false
		}
		if !decodeArgFromOp(proto, 3, int(callA)+3, &arg2IsK, &arg2K, &arg2Reg) {
			return shapeInfo{}, false
		}
		if !decodeArgFromOp(proto, 4, int(callA)+4, &arg3IsK, &arg3K, &arg3Reg) {
			return shapeInfo{}, false
		}
		cA := bytecode.A(proto.Code[5])
		cB := bytecode.B(proto.Code[5])
		cC := bytecode.C(proto.Code[5])
		// cC=1 void(0 返)/ cC=3,4 N=2,3 返 drop multi-ret(`local a,b=t:m(K,K,K)`类)
		if cA != int(callA) || cB != 5 || (cC != 1 && cC != 3 && cC != 4) {
			return shapeInfo{}, false
		}
		if bytecode.Op(proto.Code[6]) != bytecode.RETURN ||
			bytecode.B(proto.Code[6]) != 1 {
			return shapeInfo{}, false
		}
		return shapeInfo{
			ok:              true,
			retA:            0,
			retB:            1,
			retPC:           6,
			preludeOp:       uint8(bytecode.CALL),
			preludeArg:      uint32(op0B),
			isCallVoid:      true,
			isCallUpval:     op0 == bytecode.GETUPVAL,
			callA:           callA,
			callB:           uint8(cB),
			callC:           uint8(cC),
			callArgCount:    3,
			callArg1IsK:     arg1IsK,
			callArg1K:       arg1K,
			callArg1RegSrc:  arg1Reg,
			callArg2IsK:     arg2IsK,
			callArg2K:       arg2K,
			callArg2RegSrc:  arg2Reg,
			callArg3IsK:     arg3IsK,
			callArg3K:       arg3K,
			callArg3RegSrc:  arg3Reg,
			isSelfCall:      true,
			selfCallA:       callA,
			selfMethodRK:    selfRK,
			selfRecvSrcReg:  uint8(op0B),
			selfRecvIsUpval: op0 == bytecode.GETUPVAL,
		}, true
	}
	return shapeInfo{}, false
}

// analyzeSelfCallForm8 处理长度 8 形态:
//   - CALL void 4 参:[2..5] LOADK/MOVE,[6] CALL B=6 C=1,[7] RETURN B=1
//   - CALL getter 3 参 1 返:[2..4] LOADK/MOVE,[5] CALL B=5 C=2,[6] RETURN A=callA B=2,[7] RETURN B=1
//   - CALL N=2/3 返 4 参 drop multi-ret:[2..5] LOADK/MOVE,[6] CALL B=6 C=3/4,[7] RETURN B=1
//   - TAILCALL 3 参:[2..4] LOADK/MOVE,[5] TAILCALL B=5 C=0,[6] RETURN A=callA B=0,[7] RETURN B=1
func analyzeSelfCallForm8(proto *bytecode.Proto, callA uint8, selfRK uint16,
	op0 bytecode.OpCode, op0B int) (shapeInfo, bool) {
	op5 := bytecode.Op(proto.Code[5])
	op6 := bytecode.Op(proto.Code[6])

	// 区分:Code[5]=CALL/TAILCALL → 3 参 getter/tail / Code[6]=CALL → 4 参 void
	if op5 == bytecode.CALL || op5 == bytecode.TAILCALL {
		var arg1IsK, arg2IsK, arg3IsK bool
		var arg1K, arg2K, arg3K uint64
		var arg1Reg, arg2Reg, arg3Reg uint8
		if !decodeArgFromOp(proto, 2, int(callA)+2, &arg1IsK, &arg1K, &arg1Reg) {
			return shapeInfo{}, false
		}
		if !decodeArgFromOp(proto, 3, int(callA)+3, &arg2IsK, &arg2K, &arg2Reg) {
			return shapeInfo{}, false
		}
		if !decodeArgFromOp(proto, 4, int(callA)+4, &arg3IsK, &arg3K, &arg3Reg) {
			return shapeInfo{}, false
		}
		cA := bytecode.A(proto.Code[5])
		cB := bytecode.B(proto.Code[5])
		cC := bytecode.C(proto.Code[5])
		if cA != int(callA) || cB != 5 {
			return shapeInfo{}, false
		}
		if op5 == bytecode.CALL {
			if cC != 2 {
				return shapeInfo{}, false
			}
			if bytecode.Op(proto.Code[6]) != bytecode.RETURN ||
				bytecode.Op(proto.Code[7]) != bytecode.RETURN ||
				bytecode.A(proto.Code[6]) != int(callA) ||
				bytecode.B(proto.Code[6]) != 2 ||
				bytecode.B(proto.Code[7]) != 1 {
				return shapeInfo{}, false
			}
			return shapeInfo{
				ok:              true,
				retA:            uint8(bytecode.A(proto.Code[6])),
				retB:            2,
				retPC:           6,
				preludeOp:       uint8(bytecode.CALL),
				preludeArg:      uint32(op0B),
				isCallVoid:      true,
				isCallUpval:     op0 == bytecode.GETUPVAL,
				callA:           callA,
				callB:           uint8(cB),
				callC:           uint8(cC),
				callArgCount:    3,
				callArg1IsK:     arg1IsK,
				callArg1K:       arg1K,
				callArg1RegSrc:  arg1Reg,
				callArg2IsK:     arg2IsK,
				callArg2K:       arg2K,
				callArg2RegSrc:  arg2Reg,
				callArg3IsK:     arg3IsK,
				callArg3K:       arg3K,
				callArg3RegSrc:  arg3Reg,
				isSelfCall:      true,
				selfCallA:       callA,
				selfMethodRK:    selfRK,
				selfRecvSrcReg:  uint8(op0B),
				selfRecvIsUpval: op0 == bytecode.GETUPVAL,
			}, true
		}
		// TAILCALL 3 参
		if cC != 0 {
			return shapeInfo{}, false
		}
		if bytecode.Op(proto.Code[6]) != bytecode.RETURN ||
			bytecode.Op(proto.Code[7]) != bytecode.RETURN ||
			bytecode.A(proto.Code[6]) != int(callA) ||
			bytecode.B(proto.Code[6]) != 0 ||
			bytecode.B(proto.Code[7]) != 1 {
			return shapeInfo{}, false
		}
		return shapeInfo{
			ok:              true,
			retA:            uint8(bytecode.A(proto.Code[6])),
			retB:            0,
			retPC:           6,
			preludeOp:       uint8(bytecode.TAILCALL),
			preludeArg:      uint32(op0B),
			isTailCall:      true,
			isCallUpval:     op0 == bytecode.GETUPVAL,
			callA:           callA,
			callB:           uint8(cB),
			callC:           uint8(cC),
			callArgCount:    3,
			callArg1IsK:     arg1IsK,
			callArg1K:       arg1K,
			callArg1RegSrc:  arg1Reg,
			callArg2IsK:     arg2IsK,
			callArg2K:       arg2K,
			callArg2RegSrc:  arg2Reg,
			callArg3IsK:     arg3IsK,
			callArg3K:       arg3K,
			callArg3RegSrc:  arg3Reg,
			isSelfCall:      true,
			selfCallA:       callA,
			selfMethodRK:    selfRK,
			selfRecvSrcReg:  uint8(op0B),
			selfRecvIsUpval: op0 == bytecode.GETUPVAL,
		}, true
	}
	// Code[6]=CALL → CALL void 4 参
	if op6 == bytecode.CALL {
		var arg1IsK, arg2IsK, arg3IsK, arg4IsK bool
		var arg1K, arg2K, arg3K, arg4K uint64
		var arg1Reg, arg2Reg, arg3Reg, arg4Reg uint8
		if !decodeArgFromOp(proto, 2, int(callA)+2, &arg1IsK, &arg1K, &arg1Reg) {
			return shapeInfo{}, false
		}
		if !decodeArgFromOp(proto, 3, int(callA)+3, &arg2IsK, &arg2K, &arg2Reg) {
			return shapeInfo{}, false
		}
		if !decodeArgFromOp(proto, 4, int(callA)+4, &arg3IsK, &arg3K, &arg3Reg) {
			return shapeInfo{}, false
		}
		if !decodeArgFromOp(proto, 5, int(callA)+5, &arg4IsK, &arg4K, &arg4Reg) {
			return shapeInfo{}, false
		}
		cA := bytecode.A(proto.Code[6])
		cB := bytecode.B(proto.Code[6])
		cC := bytecode.C(proto.Code[6])
		// cC=1 void(0 返)/ cC=3,4 N=2,3 返 drop multi-ret(`local a,b=t:m(K,K,K,K)`类)
		if cA != int(callA) || cB != 6 || (cC != 1 && cC != 3 && cC != 4) {
			return shapeInfo{}, false
		}
		if bytecode.Op(proto.Code[7]) != bytecode.RETURN ||
			bytecode.B(proto.Code[7]) != 1 {
			return shapeInfo{}, false
		}
		return shapeInfo{
			ok:              true,
			retA:            0,
			retB:            1,
			retPC:           7,
			preludeOp:       uint8(bytecode.CALL),
			preludeArg:      uint32(op0B),
			isCallVoid:      true,
			isCallUpval:     op0 == bytecode.GETUPVAL,
			callA:           callA,
			callB:           uint8(cB),
			callC:           uint8(cC),
			callArgCount:    4,
			callArg1IsK:     arg1IsK,
			callArg1K:       arg1K,
			callArg1RegSrc:  arg1Reg,
			callArg2IsK:     arg2IsK,
			callArg2K:       arg2K,
			callArg2RegSrc:  arg2Reg,
			callArg3IsK:     arg3IsK,
			callArg3K:       arg3K,
			callArg3RegSrc:  arg3Reg,
			callArg4IsK:     arg4IsK,
			callArg4K:       arg4K,
			callArg4RegSrc:  arg4Reg,
			isSelfCall:      true,
			selfCallA:       callA,
			selfMethodRK:    selfRK,
			selfRecvSrcReg:  uint8(op0B),
			selfRecvIsUpval: op0 == bytecode.GETUPVAL,
		}, true
	}
	return shapeInfo{}, false
}

// analyzeSelfCallForm9 处理长度 9 形态:CALL void 5 参 / CALL getter 4 参 1 返 /
// CALL N=2/3 返 5 参 drop multi-ret / TAILCALL 4 参。
func analyzeSelfCallForm9(proto *bytecode.Proto, callA uint8, selfRK uint16,
	op0 bytecode.OpCode, op0B int) (shapeInfo, bool) {
	op6 := bytecode.Op(proto.Code[6])
	op7 := bytecode.Op(proto.Code[7])

	if op6 == bytecode.CALL || op6 == bytecode.TAILCALL {
		var arg1IsK, arg2IsK, arg3IsK, arg4IsK bool
		var arg1K, arg2K, arg3K, arg4K uint64
		var arg1Reg, arg2Reg, arg3Reg, arg4Reg uint8
		if !decodeArgFromOp(proto, 2, int(callA)+2, &arg1IsK, &arg1K, &arg1Reg) {
			return shapeInfo{}, false
		}
		if !decodeArgFromOp(proto, 3, int(callA)+3, &arg2IsK, &arg2K, &arg2Reg) {
			return shapeInfo{}, false
		}
		if !decodeArgFromOp(proto, 4, int(callA)+4, &arg3IsK, &arg3K, &arg3Reg) {
			return shapeInfo{}, false
		}
		if !decodeArgFromOp(proto, 5, int(callA)+5, &arg4IsK, &arg4K, &arg4Reg) {
			return shapeInfo{}, false
		}
		cA := bytecode.A(proto.Code[6])
		cB := bytecode.B(proto.Code[6])
		cC := bytecode.C(proto.Code[6])
		if cA != int(callA) || cB != 6 {
			return shapeInfo{}, false
		}
		if op6 == bytecode.CALL {
			if cC != 2 {
				return shapeInfo{}, false
			}
			if bytecode.Op(proto.Code[7]) != bytecode.RETURN ||
				bytecode.Op(proto.Code[8]) != bytecode.RETURN ||
				bytecode.A(proto.Code[7]) != int(callA) ||
				bytecode.B(proto.Code[7]) != 2 ||
				bytecode.B(proto.Code[8]) != 1 {
				return shapeInfo{}, false
			}
			return shapeInfo{
				ok:              true,
				retA:            uint8(bytecode.A(proto.Code[7])),
				retB:            2,
				retPC:           7,
				preludeOp:       uint8(bytecode.CALL),
				preludeArg:      uint32(op0B),
				isCallVoid:      true,
				isCallUpval:     op0 == bytecode.GETUPVAL,
				callA:           callA,
				callB:           uint8(cB),
				callC:           uint8(cC),
				callArgCount:    4,
				callArg1IsK:     arg1IsK,
				callArg1K:       arg1K,
				callArg1RegSrc:  arg1Reg,
				callArg2IsK:     arg2IsK,
				callArg2K:       arg2K,
				callArg2RegSrc:  arg2Reg,
				callArg3IsK:     arg3IsK,
				callArg3K:       arg3K,
				callArg3RegSrc:  arg3Reg,
				callArg4IsK:     arg4IsK,
				callArg4K:       arg4K,
				callArg4RegSrc:  arg4Reg,
				isSelfCall:      true,
				selfCallA:       callA,
				selfMethodRK:    selfRK,
				selfRecvSrcReg:  uint8(op0B),
				selfRecvIsUpval: op0 == bytecode.GETUPVAL,
			}, true
		}
		// TAILCALL 4 参
		if cC != 0 {
			return shapeInfo{}, false
		}
		if bytecode.Op(proto.Code[7]) != bytecode.RETURN ||
			bytecode.Op(proto.Code[8]) != bytecode.RETURN ||
			bytecode.A(proto.Code[7]) != int(callA) ||
			bytecode.B(proto.Code[7]) != 0 ||
			bytecode.B(proto.Code[8]) != 1 {
			return shapeInfo{}, false
		}
		return shapeInfo{
			ok:              true,
			retA:            uint8(bytecode.A(proto.Code[7])),
			retB:            0,
			retPC:           7,
			preludeOp:       uint8(bytecode.TAILCALL),
			preludeArg:      uint32(op0B),
			isTailCall:      true,
			isCallUpval:     op0 == bytecode.GETUPVAL,
			callA:           callA,
			callB:           uint8(cB),
			callC:           uint8(cC),
			callArgCount:    4,
			callArg1IsK:     arg1IsK,
			callArg1K:       arg1K,
			callArg1RegSrc:  arg1Reg,
			callArg2IsK:     arg2IsK,
			callArg2K:       arg2K,
			callArg2RegSrc:  arg2Reg,
			callArg3IsK:     arg3IsK,
			callArg3K:       arg3K,
			callArg3RegSrc:  arg3Reg,
			callArg4IsK:     arg4IsK,
			callArg4K:       arg4K,
			callArg4RegSrc:  arg4Reg,
			isSelfCall:      true,
			selfCallA:       callA,
			selfMethodRK:    selfRK,
			selfRecvSrcReg:  uint8(op0B),
			selfRecvIsUpval: op0 == bytecode.GETUPVAL,
		}, true
	}
	// Code[7]=CALL → CALL void 5 参
	if op7 == bytecode.CALL {
		var arg1IsK, arg2IsK, arg3IsK, arg4IsK, arg5IsK bool
		var arg1K, arg2K, arg3K, arg4K, arg5K uint64
		var arg1Reg, arg2Reg, arg3Reg, arg4Reg, arg5Reg uint8
		if !decodeArgFromOp(proto, 2, int(callA)+2, &arg1IsK, &arg1K, &arg1Reg) {
			return shapeInfo{}, false
		}
		if !decodeArgFromOp(proto, 3, int(callA)+3, &arg2IsK, &arg2K, &arg2Reg) {
			return shapeInfo{}, false
		}
		if !decodeArgFromOp(proto, 4, int(callA)+4, &arg3IsK, &arg3K, &arg3Reg) {
			return shapeInfo{}, false
		}
		if !decodeArgFromOp(proto, 5, int(callA)+5, &arg4IsK, &arg4K, &arg4Reg) {
			return shapeInfo{}, false
		}
		if !decodeArgFromOp(proto, 6, int(callA)+6, &arg5IsK, &arg5K, &arg5Reg) {
			return shapeInfo{}, false
		}
		cA := bytecode.A(proto.Code[7])
		cB := bytecode.B(proto.Code[7])
		cC := bytecode.C(proto.Code[7])
		// cC=1 void(0 返)/ cC=3,4 N=2,3 返 drop multi-ret(`local a,b=t:m(K×5)`类)
		if cA != int(callA) || cB != 7 || (cC != 1 && cC != 3 && cC != 4) {
			return shapeInfo{}, false
		}
		if bytecode.Op(proto.Code[8]) != bytecode.RETURN ||
			bytecode.B(proto.Code[8]) != 1 {
			return shapeInfo{}, false
		}
		return shapeInfo{
			ok:              true,
			retA:            0,
			retB:            1,
			retPC:           8,
			preludeOp:       uint8(bytecode.CALL),
			preludeArg:      uint32(op0B),
			isCallVoid:      true,
			isCallUpval:     op0 == bytecode.GETUPVAL,
			callA:           callA,
			callB:           uint8(cB),
			callC:           uint8(cC),
			callArgCount:    5,
			callArg1IsK:     arg1IsK,
			callArg1K:       arg1K,
			callArg1RegSrc:  arg1Reg,
			callArg2IsK:     arg2IsK,
			callArg2K:       arg2K,
			callArg2RegSrc:  arg2Reg,
			callArg3IsK:     arg3IsK,
			callArg3K:       arg3K,
			callArg3RegSrc:  arg3Reg,
			callArg4IsK:     arg4IsK,
			callArg4K:       arg4K,
			callArg4RegSrc:  arg4Reg,
			callArg5IsK:     arg5IsK,
			callArg5K:       arg5K,
			callArg5RegSrc:  arg5Reg,
			isSelfCall:      true,
			selfCallA:       callA,
			selfMethodRK:    selfRK,
			selfRecvSrcReg:  uint8(op0B),
			selfRecvIsUpval: op0 == bytecode.GETUPVAL,
		}, true
	}
	return shapeInfo{}, false
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

	// PJ5 CALL void 形态(MOVE/GETUPVAL+CALL+RETURN void 长度 3 或 4)
	// 在长度分流前先 try——既支持 0 参形态 (codeLen=3) 也支持 1 K 参形态
	// (codeLen=4)。
	if cv, ok := analyzeCallVoidForm(proto); ok {
		return cv
	}

	// PJ5 TAILCALL 形态(MOVE/GETUPVAL+...+TAILCALL+dead RETURN B=0+RETURN B=1
	// 长度 4/5/6)在长度分流前先 try。luac stmtReturn 单 CallExpr 快路径产物。
	if tc, ok := analyzeTailCallForm(proto); ok {
		return tc
	}

	// PJ5 SELF method call 形态(MOVE/GETUPVAL + SELF + ... + CALL/TAILCALL + RETURN
	// 长度 4..6)在长度分流前先 try。`obj:method(args)` 的 luac 编译形态。
	if sc, ok := analyzeSelfCallForm(proto); ok {
		return sc
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
		// 长度 8/9:可能是 PJ3 FORLOOP body 含 reg-K op 形态
		if floopBody, ok := analyzeForLoopBodyForm(proto); ok {
			return floopBody
		}
		// 长度 9/10:可能是 PJ3 FORLOOP body2 二段 reg-K op 形态
		if floopBody2, ok := analyzeForLoopBody2Form(proto); ok {
			return floopBody2
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
		// **NOT 单独 case 处理**(`function(x) return not x end` 形态):见
		// 下方 `case bytecode.NOT` 分支——经 host.GetReg(B) 读 R(B) +
		// SetReg(A, BoolValue(!Truthy(R(B)))),pure Truthy 无 metamethod、
		// 无 raise,与 UNM/LEN 慢路径解耦故不并入本 case。
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
	// **PJ4 IC ArrayHit 优先识别**(承 03 §6 stableShape/Index 直达槽)。
	//
	// **必须先尝试 IC inline(在 analyzeShape 之前判)**:IC 形态长度 2/3 与
	// analyzeShape 的 GETTABLE host helper 形态(GETTABLE+RETURN A 2)字节
	// 完全重叠——若不先识别 IC,analyzeShape 的 GETTABLE case 会立即匹配并
	// 把本 proto 路由到 host.GetTable 慢路径(byte-equal P1 解释器,但无字节
	// 级直达加速)。
	//
	// IC 触发条件比 GETTABLE host helper 严格 4 倍:
	//   - proto.IC[0].Kind = ArrayHit(P1 解释器观测过 array 命中,不是
	//     None / NodeHit / MonoMeta)
	//   - feedback.Points[0].Kind = FBTableMono(P2 聚合确认 mono)
	//   - feedback.Points[0].Confidence >= 0.99(投机阈值)
	//   - feedback / proto.IC stableShape & stableIndex 一致
	//   - C 字段 >= 256(K 常量索引,不是动态 reg)
	//
	// 任一不满足 → analyzeGetTableArrayHit 返 false → fall through 到
	// analyzeShape,GETTABLE case 路由到 host.GetTable byte-equal 慢路径
	// (正确性兜底)。
	//
	// 文档引用:[[03-speculation-ic.md]] §6 ArrayHit 直达槽 + 本仓
	// gibbous_pj4_table_e2e_test.go::TestPJ4_TableArrayHit_E2E_WarmupThenForce
	// 实证 IC inline 路径 SpecTableHits 真增长。
	if archSupportsSpec() && c.hostState != nil && c.hostState.ArenaBaseAddr() != 0 {
		if icInfo, ok := analyzeGetTableArrayHit(proto, feedback); ok {
			return c.compileIcArrayHit(proto, icInfo)
		}
		// **PJ4 IC NodeHit 形态**:hash 段直达(`t["x"]` 形态),复用 ArrayHit
		// 同款 IC + feedback 双校验,差异是 IC[0].Kind=NodeHit + 编译期固化
		// stableKey from proto.Consts。NodeHit 模板 159 字节(比 ArrayHit
		// 多 key 比对段 27 字节);命中即字节级 inline,失败 fall through 到
		// host.GetTable byte-equal P1。
		if icInfo, ok := analyzeGetTableNodeHit(proto, feedback); ok {
			return c.compileIcNodeHit(proto, icInfo)
		}
		// **PJ4 SETTABLE IC ArrayHit 形态**:`function(t,v) t[K] = v end`
		// (setter,数字键 in array 段),113 字节模板反向写 array[stableIndex]
		// = R(C);失败 fall through 到 host.SetTable byte-equal(经 __newindex
		// 元方法链)。
		if icInfo, ok := analyzeSetTableArrayHit(proto, feedback); ok {
			return c.compileIcSetArrayHit(proto, icInfo)
		}
		// **PJ4 SELF IC ArrayHit 形态**:`local m = obj:method` 等 SELF + RETURN
		// 形态(罕见但有效),139 字节模板:R(A+1) := R(B) + array[stableIndex]
		// load → R(A);失败 fall through 到 host.GetTable byte-equal(R(A+1)
		// 已 store,P1 SELF case 同款步骤 byte-equal)。
		if icInfo, ok := analyzeSelfArrayHit(proto, feedback); ok {
			return c.compileIcSelfArrayHit(proto, icInfo)
		}
		// **PJ4 SETTABLE IC NodeHit 形态**:`function(t,v) t["x"] = v end`
		// (setter,字符串/任意键 in hash 段),140 字节模板反向写
		// node[stableIndex].val = R(C);失败 fall through 到 host.SetTable
		// byte-equal(经 icSetTable + __newindex 元方法链)。
		if icInfo, ok := analyzeSetTableNodeHit(proto, feedback); ok {
			return c.compileIcSetNodeHit(proto, icInfo)
		}
		// **PJ4 SELF IC NodeHit 形态**:`local m = obj:method` 等 SELF+RETURN
		// 形态,method 是字符串 ident → hash 段命中(real-world obj:method()
		// 典型形态),166 字节模板:R(A+1) := R(B) + NodeKey 比对 + NodeVal
		// load → R(A);失败 fall through 到 host.GetTable byte-equal(R(A+1)
		// 已 store,P1 SELF case 同款步骤)。
		if icInfo, ok := analyzeSelfNodeHit(proto, feedback); ok {
			return c.compileIcSelfNodeHit(proto, icInfo)
		}
		// **PJ5 SELF + CALL spec template 形态**(承 §9.10 复用 + §9.17 升级):
		// `function(o) o:m() end` 真实 OOP 调用(SELF + CALL + RETURN void),IC
		// NodeHit 命中时 SELF 段走字节级 EmitSelfNodeHit(跳过 host.Self),CALL
		// 段仍走 host.CallBaseline;失败 deopt 降级 host.Self。比 PJ4 SELF NodeHit
		// 多一步 CALL,故单独 Compile 路径。
		if icInfo, ok := analyzeSelfCallSpecForm(proto, feedback); ok {
			return c.compileSpecSelfCall(proto, icInfo)
		}
	}

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
	//
	// **arch 闸门**:用 `archSupportsForLoop()` 而非 `archSupportsSpec()`,
	// 因 FORLOOP 经 `archCallJITFull` 主路径(不经 spec trampoline);
	// arm64 端 archSupportsSpec=false 不应阻塞 FORLOOP arm64 emitter 调用
	// (本会话 stub→真接入 PJ3 全四形态后,arm64 archSupportsForLoop 已可
	// 返 true 启用)。
	if info.isForLoop && archSupportsForLoop() &&
		c.hostState != nil && c.hostState.ArenaBaseAddr() != 0 {
		var buf []byte
		// safepoint check 接入 — preemptFlag 字段偏移传给模板,模板在
		// loop body 末尾插「cmp byte [r15+pfOff], 0; jne after_loop」
		// (承 05 §1.2.2 抢占纪律 + V18 -race);trampoline 已装 r15。
		pfOff := int32(JITContextPreemptFlagOffset)

		// **hasBody2 = true:二段 body 形态**(`local s; for i=K1,K2 do
		// s = s op1 K3; s = s op2 K4 end; return s`):154 字节模板复用
		// xmm3 跨两段 SSE op,节省一次 load/store。优先于 hasBody 单 op
		// 路径判定(因 hasBody2 是 hasBody 的扩展)。
		//
		// **spec trampoline 守卫**:body/body2/RegLimit 三路径需 spec
		// trampoline 装 vsBase 到 callee-saved 寄存器(amd64 rbx / arm64
		// x26)才能寻址值栈 R(aS)。arm64 callJITFull trampoline 不装
		// x26 → 必经 archCallJITSpec;`archSupportsSpec()=false` 时
		// (arm64 当前)直接返 ErrCompileUnsupportedShape,让 Tier 框架
		// 退回解释器,**避免 fallthrough 到 LoadKReturn 静默错果**
		// (承上一轮 review 真 bug 教训)。
		if (info.hasBody2 || info.hasBody || info.forLimitIsReg) && !archSupportsSpec() {
			return nil, ErrCompileUnsupportedShape
		}

		if info.hasBody2 {
			buf = archEmitForLoopWithBody2(buf, info.forBodyKS, info.forInitK,
				info.forLimitK, info.forStepK,
				info.bodyKValue, info.bodyKValue2,
				info.forBodyAS, info.bodyOp, info.bodyOp2, pfOff)
			page, err := archMmapCode(buf)
			if err != nil {
				return nil, err
			}
			incSpecForLoopHits()
			return &p4Code{
				proto:         proto,
				codePage:      page,
				jitCtx:        NewJITContext(),
				retA:          info.retA,
				retB:          info.retB,
				retPC:         info.retPC,
				writeRetA:     false,
				host:          c.hostState,
				useSpec:       true,
				specDeoptCode: 0xFFFCDEAD_DEADFFFF,
			}, nil
		}

		// **hasBody = true:body 含 reg-K op 形态**(`local s=K; for i=K1,K2 do
		// s = s op K3 end; return s`)。135 字节模板:init R(aS)=K_s +
		// FORLOOP setup + body inline(load s / mov K_body / sseOp / store
		// s)+ safepoint + backward jmp + ret。**writeRetA=false**(body
		// 已 movsd [rbx+aS*8] xmm3 写好 R(aS)= s,host.DoReturn 取它返回)。
		if info.hasBody {
			buf = archEmitForLoopWithBody(buf, info.forBodyKS, info.forInitK,
				info.forLimitK, info.forStepK, info.bodyKValue,
				info.forBodyAS, info.bodyOp, pfOff)
			page, err := archMmapCode(buf)
			if err != nil {
				return nil, err
			}
			incSpecForLoopHits()
			return &p4Code{
				proto:     proto,
				codePage:  page,
				jitCtx:    NewJITContext(),
				retA:      info.retA,
				retB:      info.retB,
				retPC:     info.retPC,
				writeRetA: false,
				host:      c.hostState,
				// 用 callJITSpec(装 rbx+r15),模板需要 rbx 寻址 R(aS)
				useSpec: true,
				// **无 deopt**:本最简 body 形态无 guard,specDeoptCode 用
				// 「永不撞」值,Run 检测 raxSpec != deoptCode 直接走正常路径。
				specDeoptCode: 0xFFFCDEAD_DEADFFFF,
			}, nil
		}

		if info.forLimitIsReg {
			// **reg-limit hot path 真接入**(`for i=1,n do end`):117 字节模板
			// 含 IsNumber guard + 浮点 loop + safepoint + deopt block。
			// useSpec=true 走 callJITSpec(装 rbx=vsBase + r15=jitCtx)。
			// deopt 路径调 host.ForPrep raise('for' limit must be a number)
			// byte-equal 解释器。
			//
			// **upvalue-limit 子形态**:forLimitUpvalIdx>0 时 Run 端先调
			// host.GetUpval(idx-1) + host.SetReg(forLimitReg, val) 把 upval
			// 值写到 reg-limit 模板期望的 R(forLimitReg) 槽,然后走 reg-limit
			// 字节级模板(guard + loop)。
			const deoptCode uint64 = 0xFFFCDEAD_DEADBE00
			buf = archEmitForLoopRegLimit(buf, info.forInitK, info.forStepK,
				info.forLimitReg, deoptCode, pfOff)
			page, err := archMmapCode(buf)
			if err != nil {
				return nil, err
			}
			incSpecForLoopHits()
			return &p4Code{
				proto:           proto,
				codePage:        page,
				jitCtx:          NewJITContext(),
				retA:            info.retA,
				retB:            info.retB,
				retPC:           info.retPC,
				writeRetA:       false,
				preludeOp:       0, // 不走 prelude switch
				host:            c.hostState,
				useSpec:         true,
				specDeoptCode:   deoptCode,
				forLoopDeopt:    true,
				forLoopA:        info.forA,
				forLoopLimitReg: info.forLimitReg,
				forLoopUpvalIdx: info.forLimitUpvalIdx,
			}, nil
		}

		// 全常量空 body FORLOOP(本批落地)
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

	// PJ5 CALL void 形态 Compile 命中(prove-the-path 白盒命中证据)。
	if info.isCallVoid {
		incSpecCallVoidHits()
	}
	// PJ5 TAILCALL 形态 Compile 命中(prove-the-path 白盒命中证据)。
	if info.isTailCall {
		incSpecTailCallHits()
	}
	// PJ5 SELF method call 形态 Compile 命中(prove-the-path 白盒命中证据)。
	if info.isSelfCall {
		incSpecSelfCallHits()
	}

	return &p4Code{
		proto:          proto,
		codePage:       page,
		jitCtx:         NewJITContext(),
		retA:           info.retA,
		retB:           info.retB,
		retPC:          info.retPC,
		writeRetA:      info.writeRetA,
		preludeOp:      info.preludeOp,
		preludeArg:     info.preludeArg,
		preludeC:       info.preludeC,
		cmpA:           info.cmpA,
		chainOp:        info.chainOp,
		chainB:         info.chainB,
		chainC:         info.chainC,
		host:           c.hostState,
		isCallVoid:     info.isCallVoid,
		isCallUpval:    info.isCallUpval,
		callA:          info.callA,
		callB:          info.callB,
		callC:          info.callC,
		callArgCount:   info.callArgCount,
		callMultiRet:   info.callMultiRet,
		callArg1IsK:    info.callArg1IsK,
		callArg1K:      info.callArg1K,
		callArg1RegSrc: info.callArg1RegSrc,
		callArg2IsK:    info.callArg2IsK,
		callArg2K:      info.callArg2K,
		callArg2RegSrc: info.callArg2RegSrc,
		callArg3IsK:    info.callArg3IsK,
		callArg3K:      info.callArg3K,
		callArg3RegSrc: info.callArg3RegSrc,
		callArg4IsK:    info.callArg4IsK,
		callArg4K:      info.callArg4K,
		callArg4RegSrc: info.callArg4RegSrc,
		callArg5IsK:    info.callArg5IsK,
		callArg5K:      info.callArg5K,
		callArg5RegSrc: info.callArg5RegSrc,
		callArg6IsK:    info.callArg6IsK,
		callArg6K:      info.callArg6K,
		callArg6RegSrc: info.callArg6RegSrc,
		callArg7IsK:    info.callArg7IsK,
		callArg7K:      info.callArg7K,
		callArg7RegSrc: info.callArg7RegSrc,
		isTailCall:     info.isTailCall,
		// PJ5 SELF inline 形态字段
		isSelfCall:      info.isSelfCall,
		selfCallA:       info.selfCallA,
		selfMethodRK:    info.selfMethodRK,
		selfRecvSrcReg:  info.selfRecvSrcReg,
		selfRecvIsUpval: info.selfRecvIsUpval,
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
//   - **长度 3 PJ5 CALL void**:MOVE A B + CALL A 1 1 + RETURN 0 1
//     (`function(g) g() end` 类——Run 端 prelude 路径调 host.CallBaseline
//     完成 baseline doCall 分派 byte-equal P1,可 ERR 冒泡)
var ErrCompileUnsupportedShape = errors.New("internal/gibbous/jit: P4 PJ7 unsupported shape (expected: single RETURN A B / single-BB MOVE|GETUPVAL|LOADK|LOADBOOL|LOADNIL|ADD..POW|UNM|LEN|NEWTABLE|GETTABLE|GETGLOBAL|SETTABLE|SETGLOBAL + RETURN A 2 (getter) / 1 (setter) / PJ5 MOVE+CALL+RETURN void)")

// compileIcArrayHit 编译 PJ4 IC ArrayHit 形态(承 analyzeGetTableArrayHit):
// emit 129 字节 IC inline 模板,失败 deopt → Run 端调 host.GetTable byte-equal P1。
//
// **deopt 路径**:Run 端检测 raxSpec==deoptCode → 调 host.GetTable(经
// IC + 哈希 + __index 元方法链,与解释器 byte-equal)。p4Code 设
// icArrayHitDeopt=true 区分 reg-limit FORLOOP 的 host.ForPrep 路径。
func (c *Compiler) compileIcArrayHit(proto *bytecode.Proto, info shapeInfo) (bridge.GibbousCode, error) {
	const deoptCode uint64 = 0xFFFCDEAD_DEADBE00
	arenaBaseOff := int32(JITContextArenaBaseOffset)
	var buf []byte
	buf = archEmitGetTableArrayHit(buf, info.icAReg, info.icBReg,
		info.icStableShape, info.icStableIndex, arenaBaseOff, deoptCode)
	page, err := archMmapCode(buf)
	if err != nil {
		return nil, err
	}
	incSpecTableHits()
	return &p4Code{
		proto:         proto,
		codePage:      page,
		jitCtx:        NewJITContext(),
		retA:          info.retA,
		retB:          info.retB,
		retPC:         info.retPC,
		writeRetA:     false, // 模板已 mov [rbx+aReg*8], rax 写好 R(A)
		preludeOp:     info.preludeOp,
		preludeArg:    info.preludeArg,
		preludeC:      info.preludeC,
		host:          c.hostState,
		useSpec:       true,
		specDeoptCode: deoptCode,
		icArrayHit:    true, // Run 端区分 deopt 路径走 host.GetTable
	}, nil
}

// compileIcNodeHit 编译 PJ4 IC NodeHit 形态(承 analyzeGetTableNodeHit):
// emit 159 字节 IC NodeHit inline 模板,失败 deopt → Run 端调 host.GetTable
// byte-equal P1。
//
// 比 compileIcArrayHit 多一个 stableKey 编译期固化参数(模板内验
// NodeKey == stableKey 防键退化)。Run deopt 路径与 ArrayHit 共用 icArrayHit
// 字段——两者都是 Run 端 raxSpec==deoptCode 时调 host.GetTable byte-equal
// (P1 解释器同款 icGetTable 路径既支持 ArrayHit 也支持 NodeHit,无需区分)。
func (c *Compiler) compileIcNodeHit(proto *bytecode.Proto, info shapeInfo) (bridge.GibbousCode, error) {
	const deoptCode uint64 = 0xFFFCDEAD_DEADBE01 // 与 ArrayHit 区分但 Run 端共用 host.GetTable 路径
	arenaBaseOff := int32(JITContextArenaBaseOffset)
	var buf []byte
	buf = archEmitGetTableNodeHit(buf, info.icAReg, info.icBReg,
		info.icStableShape, info.icStableIndex, info.icStableKey,
		arenaBaseOff, deoptCode)
	page, err := archMmapCode(buf)
	if err != nil {
		return nil, err
	}
	incSpecTableHits() // 复用 SpecTableHits 探针(ArrayHit + NodeHit 共计)
	return &p4Code{
		proto:         proto,
		codePage:      page,
		jitCtx:        NewJITContext(),
		retA:          info.retA,
		retB:          info.retB,
		retPC:         info.retPC,
		writeRetA:     false, // 模板已 mov [rbx+aReg*8], rax 写好 R(A)
		preludeOp:     info.preludeOp,
		preludeArg:    info.preludeArg,
		preludeC:      info.preludeC,
		host:          c.hostState,
		useSpec:       true,
		specDeoptCode: deoptCode,
		icArrayHit:    true, // Run 端共用 host.GetTable 路径(P1 icGetTable 兼容)
	}, nil
}

// compileIcSetArrayHit 编译 PJ4 SETTABLE IC ArrayHit 形态(承 analyzeSetTableArrayHit):
// emit 113 字节 SETTABLE IC inline 反向写模板,失败 deopt → Run 端调
// host.SetTable byte-equal P1(经 icSetTable + __newindex 元方法链)。
//
// **setter 形态 retB=1**(SETTABLE 0 返回值)— Run 端 DoReturn 不读 R(A)。
//
// 模板 113 字节:严密 IsTable guard + arena base + gen check + arrayRef
// + load R(C) value → rdx + 反向 store mov [r14+rcx+stableIndex*8], rdx +
// ret + deopt block。**简化**:本批不验现有 array[stableIndex] != nil
// (防新键路径)+ 不验 __newindex 元表(详 EmitSetTableArrayHit godoc)。
func (c *Compiler) compileIcSetArrayHit(proto *bytecode.Proto, info shapeInfo) (bridge.GibbousCode, error) {
	const deoptCode uint64 = 0xFFFCDEAD_DEADBE02
	arenaBaseOff := int32(JITContextArenaBaseOffset)
	var buf []byte
	buf = archEmitSetTableArrayHit(buf, info.icAReg, info.icSetCReg,
		info.icStableShape, info.icStableIndex, arenaBaseOff, deoptCode)
	page, err := archMmapCode(buf)
	if err != nil {
		return nil, err
	}
	incSpecTableHits() // 复用 SpecTableHits 探针(ArrayHit + NodeHit + SETTABLE 共计)
	return &p4Code{
		proto:         proto,
		codePage:      page,
		jitCtx:        NewJITContext(),
		retA:          info.retA,
		retB:          info.retB, // setter retB=1
		retPC:         info.retPC,
		writeRetA:     false, // setter 无 R(A) 写
		preludeOp:     info.preludeOp,
		preludeArg:    info.preludeArg,
		preludeC:      info.preludeC,
		host:          c.hostState,
		useSpec:       true,
		specDeoptCode: deoptCode,
		icSetArrayHit: true, // Run 端 deopt 走 host.SetTable
	}, nil
}

// compileIcSelfArrayHit 编译 PJ4 SELF IC ArrayHit 形态(承 analyzeSelfArrayHit):
// emit 139 字节 SELF IC inline 模板(GETTABLE ArrayHit 132 + R(A+1) 拷段 7),
// 失败 deopt → Run 端调 host.GetTable byte-equal P1(R(A+1) 已 store,
// P1 SELF case 同款步骤 byte-equal)。
//
// **SELF 形态 retB=2**(SELF + RETURN A 2 取 R(A))。R(A+1) 由模板从 R(B)
// 拷写,deopt 路径不需回滚 R(A+1)— P1 SELF 路径同样先 setReg(A+1, B)。
func (c *Compiler) compileIcSelfArrayHit(proto *bytecode.Proto, info shapeInfo) (bridge.GibbousCode, error) {
	const deoptCode uint64 = 0xFFFCDEAD_DEADBE03
	arenaBaseOff := int32(JITContextArenaBaseOffset)
	var buf []byte
	buf = archEmitSelfArrayHit(buf, info.icAReg, info.icBReg,
		info.icStableShape, info.icStableIndex, arenaBaseOff, deoptCode)
	page, err := archMmapCode(buf)
	if err != nil {
		return nil, err
	}
	incSpecTableHits() // 复用 SpecTableHits 探针(全 PJ4 路径共计)
	return &p4Code{
		proto:         proto,
		codePage:      page,
		jitCtx:        NewJITContext(),
		retA:          info.retA,
		retB:          info.retB,
		retPC:         info.retPC,
		writeRetA:     false, // 模板已写 R(A)
		preludeOp:     info.preludeOp,
		preludeArg:    info.preludeArg,
		preludeC:      info.preludeC,
		host:          c.hostState,
		useSpec:       true,
		specDeoptCode: deoptCode,
		icArrayHit:    true, // SELF deopt 复用 host.GetTable 路径(P1 SELF case 已先 setReg(A+1, B))
	}, nil
}

// compileIcSetNodeHit 编译 PJ4 SETTABLE IC NodeHit 形态(承 analyzeSetTableNodeHit):
// emit 140 字节 SETTABLE NodeHit IC inline 反向写模板(GetTable NodeHit
// 159 - getter 段 34 + setter 段 15),失败 deopt → Run 端调 host.SetTable
// byte-equal P1(经 icSetTable + __newindex 元方法链)。
//
// **setter 形态 retB=1**,Run 端 DoReturn 不读 R(A)。
//
// 模板 140 字节:严密 IsTable guard + arena base + gen check + nodeRef
// + node[stableIndex] + key 比对 + load R(C) → rdx + 反向 store NodeVal
// + ret + deopt block。设计简化同 SetTable ArrayHit:无 __newindex / 不
// 验现有 NodeVal(详 EmitSetTableNodeHit godoc)。
func (c *Compiler) compileIcSetNodeHit(proto *bytecode.Proto, info shapeInfo) (bridge.GibbousCode, error) {
	const deoptCode uint64 = 0xFFFCDEAD_DEADBE04
	arenaBaseOff := int32(JITContextArenaBaseOffset)
	var buf []byte
	buf = archEmitSetTableNodeHit(buf, info.icAReg, info.icSetCReg,
		info.icStableShape, info.icStableIndex, info.icStableKey,
		arenaBaseOff, deoptCode)
	page, err := archMmapCode(buf)
	if err != nil {
		return nil, err
	}
	incSpecTableHits() // 复用 SpecTableHits 探针(全 PJ4 路径共计)
	return &p4Code{
		proto:         proto,
		codePage:      page,
		jitCtx:        NewJITContext(),
		retA:          info.retA,
		retB:          info.retB, // setter retB=1
		retPC:         info.retPC,
		writeRetA:     false, // setter 无 R(A) 写
		preludeOp:     info.preludeOp,
		preludeArg:    info.preludeArg,
		preludeC:      info.preludeC,
		host:          c.hostState,
		useSpec:       true,
		specDeoptCode: deoptCode,
		icSetArrayHit: true, // Run 端 deopt 复用 host.SetTable 路径(P1 icSetTable 兼容 ArrayHit+NodeHit)
	}, nil
}

// compileIcSelfNodeHit 编译 PJ4 SELF IC NodeHit 形态(承 analyzeSelfNodeHit):
// emit 166 字节 SELF NodeHit IC inline 模板(SELF ArrayHit 139 + key 比对
// 段 27),失败 deopt → Run 端调 host.GetTable byte-equal P1(R(A+1) 已
// store,P1 SELF case 同款步骤;P1 icGetTable 兼容 NodeHit)。
//
// **SELF 形态 retB=2**(取 R(A) method 函数)。R(A+1) 由模板从 R(B) 拷写,
// deopt 路径不需回滚——P1 SELF 路径同样先 setReg(A+1, B)。这是 real-world
// `obj:method()` 调用的典型形态(method 是字符串 ident)。
func (c *Compiler) compileIcSelfNodeHit(proto *bytecode.Proto, info shapeInfo) (bridge.GibbousCode, error) {
	const deoptCode uint64 = 0xFFFCDEAD_DEADBE05
	arenaBaseOff := int32(JITContextArenaBaseOffset)
	var buf []byte
	buf = archEmitSelfNodeHit(buf, info.icAReg, info.icBReg,
		info.icStableShape, info.icStableIndex, info.icStableKey,
		arenaBaseOff, deoptCode)
	page, err := archMmapCode(buf)
	if err != nil {
		return nil, err
	}
	incSpecTableHits() // 复用 SpecTableHits 探针(全 PJ4 路径共计)
	return &p4Code{
		proto:         proto,
		codePage:      page,
		jitCtx:        NewJITContext(),
		retA:          info.retA,
		retB:          info.retB,
		retPC:         info.retPC,
		writeRetA:     false, // 模板已写 R(A)
		preludeOp:     info.preludeOp,
		preludeArg:    info.preludeArg,
		preludeC:      info.preludeC,
		host:          c.hostState,
		useSpec:       true,
		specDeoptCode: deoptCode,
		icArrayHit:    true, // SELF deopt 复用 host.GetTable 路径(P1 SELF case 已 setReg(A+1, B))
	}, nil
}

// compileSpecSelfCall 编译 PJ5 SELF + CALL spec template 形态(承
// analyzeSelfCallSpecForm + §9.10 PJ4 EmitSelfNodeHit 复用):
//
// emit 166 字节 SELF NodeHit IC inline 模板(SELF 段:IC NodeHit guard +
// stableKey 比对 + NodeVal store R(callA)=method + store R(callA+1)=self),
// 失败 deopt → Run 端降级 host.Self;**成功后 Run 端继续走 host.CallBaseline +
// host.DoReturn 完成 CALL 段**(与 PJ4 SELF NodeHit 的差异:多一步 CALL)。
//
// **Run 端预处理**(承 code.go::runSpecSelfCall):callJITSpec 之前先
// host.GetReg/GetUpval + SetReg 装 R(callA)=recv(模拟 MOVE/GETUPVAL,
// 因 spec 段从 R(callA) 字节级读 receiver)。
func (c *Compiler) compileSpecSelfCall(proto *bytecode.Proto, info shapeInfo) (bridge.GibbousCode, error) {
	const deoptCode uint64 = 0xFFFCDEAD_DEADBE06
	arenaBaseOff := int32(JITContextArenaBaseOffset)
	var buf []byte
	// PJ5 SELF + CALL spec template:**args 装载段 emit 在 SELF 段之前**(承
	// §9.19 摊薄实测 3 参形态 ratio 1.017x → 1.x 慢的瓶颈是 args 装载的 host
	// round-trip)。args 装载到 R(callA+2..callA+1+N)字节级直发 mov,跳过
	// host.GetReg/SetReg N 次跨 Go round-trip。SELF 段执行后 method/self 已落
	// R(callA)/R(callA+1),args 段已装到 R(callA+2..),host.CallBaseline 调用
	// 时 args 已就位。
	//
	// **recv 装载**(MOVE form,form M*):字节级 inline R(callA)=R(srcReg)
	// 跳过 host.GetReg + SetReg 2 次跨界;GETUPVAL form(form U*)留 Run 端
	// host helper round-trip(upvalue 不在 vsBase 栈,需要复杂 closure 寻址)。
	//
	// 槽位不冲突:args 写 R(callA+2..callA+1+N);recv 段写 R(callA)=R(srcReg);
	// SELF 段读 R(callA)=recv + 写 R(callA+1)=self + 写 R(callA)=method。
	// 顺序:recv inline → args inline → SELF inline → ret(args+recv 段 ret 失败
	// 路径 deopt 时已执行完,host.Self 降级时仍可用)。
	callA := info.callA
	if !info.selfRecvIsUpval {
		// MOVE recv:字节级 R(callA) = R(srcReg),省 host.GetReg+SetReg 2 跨界
		buf = archEmitSpecArgLoadReg(buf, callA, info.selfRecvSrcReg)
	}
	if info.callArgCount >= 1 {
		dst := callA + 2 + 0
		if info.callArg1IsK {
			buf = archEmitSpecArgLoadK(buf, dst, info.callArg1K)
		} else {
			buf = archEmitSpecArgLoadReg(buf, dst, info.callArg1RegSrc)
		}
	}
	if info.callArgCount >= 2 {
		dst := callA + 2 + 1
		if info.callArg2IsK {
			buf = archEmitSpecArgLoadK(buf, dst, info.callArg2K)
		} else {
			buf = archEmitSpecArgLoadReg(buf, dst, info.callArg2RegSrc)
		}
	}
	if info.callArgCount >= 3 {
		dst := callA + 2 + 2
		if info.callArg3IsK {
			buf = archEmitSpecArgLoadK(buf, dst, info.callArg3K)
		} else {
			buf = archEmitSpecArgLoadReg(buf, dst, info.callArg3RegSrc)
		}
	}
	if info.callArgCount >= 4 {
		dst := callA + 2 + 3
		if info.callArg4IsK {
			buf = archEmitSpecArgLoadK(buf, dst, info.callArg4K)
		} else {
			buf = archEmitSpecArgLoadReg(buf, dst, info.callArg4RegSrc)
		}
	}
	if info.callArgCount >= 5 {
		dst := callA + 2 + 4
		if info.callArg5IsK {
			buf = archEmitSpecArgLoadK(buf, dst, info.callArg5K)
		} else {
			buf = archEmitSpecArgLoadReg(buf, dst, info.callArg5RegSrc)
		}
	}
	if info.callArgCount >= 6 {
		dst := callA + 2 + 5
		if info.callArg6IsK {
			buf = archEmitSpecArgLoadK(buf, dst, info.callArg6K)
		} else {
			buf = archEmitSpecArgLoadReg(buf, dst, info.callArg6RegSrc)
		}
	}
	if info.callArgCount >= 7 {
		dst := callA + 2 + 6
		if info.callArg7IsK {
			buf = archEmitSpecArgLoadK(buf, dst, info.callArg7K)
		} else {
			buf = archEmitSpecArgLoadReg(buf, dst, info.callArg7RegSrc)
		}
	}
	buf = archEmitSelfNodeHit(buf, info.icAReg, info.icBReg,
		info.icStableShape, info.icStableIndex, info.icStableKey,
		arenaBaseOff, deoptCode)
	page, err := archMmapCode(buf)
	if err != nil {
		return nil, err
	}
	incSpecSelfCallHits()     // PJ5 SELF inline 命中(prove-the-path 复用 SELF 探针)
	incSpecSelfCallSpecHits() // PJ5 SELF + CALL spec template 专属命中
	onP4Install(proto)        // 注册 p4SpecState[proto] = P4Speculative(承 §9.18 + 04 §5.2)
	return &p4Code{
		proto:           proto,
		codePage:        page,
		jitCtx:          NewJITContext(),
		retA:            info.retA,
		retB:            info.retB,
		retPC:           info.retPC,
		writeRetA:       false, // 模板已写 R(callA)
		preludeOp:       info.preludeOp,
		preludeArg:      info.preludeArg,
		host:            c.hostState,
		useSpec:         true,
		specDeoptCode:   deoptCode,
		isCallVoid:      info.isCallVoid,
		isCallUpval:     info.isCallUpval,
		callA:           info.callA,
		callB:           info.callB,
		callC:           info.callC,
		callArgCount:    info.callArgCount,
		callArg1IsK:     info.callArg1IsK,
		callArg1K:       info.callArg1K,
		callArg1RegSrc:  info.callArg1RegSrc,
		callArg2IsK:     info.callArg2IsK,
		callArg2K:       info.callArg2K,
		callArg2RegSrc:  info.callArg2RegSrc,
		callArg3IsK:     info.callArg3IsK,
		callArg3K:       info.callArg3K,
		callArg3RegSrc:  info.callArg3RegSrc,
		callArg4IsK:     info.callArg4IsK,
		callArg4K:       info.callArg4K,
		callArg4RegSrc:  info.callArg4RegSrc,
		callArg5IsK:     info.callArg5IsK,
		callArg5K:       info.callArg5K,
		callArg5RegSrc:  info.callArg5RegSrc,
		callArg6IsK:     info.callArg6IsK,
		callArg6K:       info.callArg6K,
		callArg6RegSrc:  info.callArg6RegSrc,
		callArg7IsK:     info.callArg7IsK,
		callArg7K:       info.callArg7K,
		callArg7RegSrc:  info.callArg7RegSrc,
		isSelfCall:      info.isSelfCall,
		selfCallA:       info.selfCallA,
		selfMethodRK:    info.selfMethodRK,
		selfRecvSrcReg:  info.selfRecvSrcReg,
		selfRecvIsUpval: info.selfRecvIsUpval,
		useSpecSelfCall: true,
	}, nil
}
