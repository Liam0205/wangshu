//go:build wangshu_p4

package jit

import (
	"errors"

	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/value"
)

// p4Code 实装 `bridge.GibbousCode` 接口(`p2-bridge/05-p3-p4-interface.md`
// §6 + p4-method-jit/00-overview.md §1 边界表 GibbousCode 实现方行)。
//
// **PJ2 真接入版**(2026-06-25):p4Code 真持有 *CodePage(mmap 段)+ jitContext
// + retA(目标寄存器号);Run 经 callJITFull 跳进 mmap 段拿 RAX,写回 stack
// 起 base/8 + retA 处。
//
// **PJ2 真接入范围**:仅支持「LOADK A K(0); RETURN A 1」单 BB 形态——这是
// spike 闸门 ⊕ trampoline ⊕ emitter 三件套唯一无副作用、无 helper、无 跨层
// 调用的 Lua 子集。其它形态由 SupportsAllOpcodes 拒(承 06 §3.8 渐进白名单
// 纪律 + p4Code.Run 不动栈/不调 helper 协议)。
//
// 与 P3 *p3Code 的对位:
//   - P3 GibbousCode 包 wazero CompiledModule + api.Function 句柄;
//   - P4 GibbousCode 包 unsafe.Pointer 原生码段(经 jitamd64.CodePage)+
//     jitContext + 编译期固化的 retA 信息。
type p4Code struct {
	proto *bytecode.Proto

	// codePage 是 mmap 出来的 PROT_RX 段(W^X 翻面后),holds 段直到 Dispose。
	// 类型别名 archCodePage 由 arch_*.go 路由(amd64/arm64 各自的 CodePage)。
	codePage *archCodePage

	// jitCtx 是本编译产物的 JIT 执行上下文(per-Proto 单例)。
	jitCtx *JITContext

	// retA 是 RETURN 指令的 A 寄存器号——p4Code.Run 的 mmap 段返回 RAX 后,
	// 把 RAX 写到 stack[base/8 + retA] 槽位(= R(retA) NaN-box)。
	retA uint8

	// retB 是 RETURN 指令的 B 字段(B-1 = 返回值个数)。
	//   - retB = 1:0 个返回值(空 RETURN);Run 不写 stack 槽
	//   - retB = 2:1 个返回值;Run 把 RAX 写到 stack[base/8 + retA]
	retB uint8

	// retPC 是 RETURN 指令的 pc(从 0 起)——DoReturn 用于物化 ci.savedPC。
	retPC uint8

	// writeRetA 标志 mmap 段执行后是否要把 RAX 写 R(retA):
	//   - true(LOADK/LOADBOOL/LOADNIL):mmap 段算出新值,需写槽位
	//   - false(首条 RETURN A B=2,如 `function(x) return x end`):R(retA)
	//     已经是参数值(由 caller 写入),不应被 mmap 段返回的 dummy RAX 覆盖
	writeRetA bool

	// preludeOp 是 RETURN 前的预备 opcode(若有,用于 P4 PJ7 简化形态调
	// host helper 取值再 SetReg 写 R(retA),或调 host.Arith 完成算术):
	//   - 0(默认):无 prelude,LOADK/LOADBOOL/LOADNIL 编译期已算出值
	//   - bytecode.GETUPVAL:Run 调 host.GetUpval(retA, preludeArg) 取值,
	//     SetReg(retA, val) 写槽。这是「mmap 段调 host」的简化替代——把
	//     host 调用从 mmap 段移到 Go 端 Run。
	//   - bytecode.ADD/SUB/MUL/DIV/MOD/POW:Run 调 host.Arith(base, pc, op,
	//     b=preludeArg, c=preludeC, a=retA) 完成算术 + 写 R(retA)。helper 内
	//     处理 RK 解码 + double-dispatch + coercion + 元方法;返 1 时 ERR
	//     冒泡(由 enterGibbous 取 pendingErr)。
	preludeOp uint8

	// preludeArg 是 prelude opcode 的 B 字段(GETUPVAL 的 upvalue 索引 0-255,
	// 或算术族的 B 字段含 RK 编码 0-511,或 GETGLOBAL/SETGLOBAL 的 Bx 字段
	// 0-262143——故升 uint32)。
	preludeArg uint32

	// preludeC 是算术族 prelude 的 C 字段(RK 编码 0-511)。GETUPVAL 形态不用。
	preludeC uint16

	// cmpA 是比较折叠形态(EQ/LT/LE)的 A 字段(0 或 1,用于折成
	// `BoolValue(packed.bit0 == cmpA)`)。其它形态不用。
	cmpA uint8

	// chainOp/chainB/chainC 是二段算术链式形态(MUL+ADD+RETURN 等)的第二
	// 段 op + B + C。0=无 chain,非零时 Run 串行调 host.Arith 两次。
	chainOp uint8
	chainB  uint16
	chainC  uint16

	// host 是注入的 P4HostState(从 *Compiler 拷贝):per-p4Code 持有,无并发
	// write(只在 Compile 时写一次,Run 时只读)——V18 -race 友好。
	host P4HostState

	// useSpec 标记本 p4Code 是否用 PJ2 投机模板(承 docs/design/p4-method-jit/
	// 03-speculation-ic.md §2 IsNumber×2 投机模板)。当前仅 ADD A B C +
	// RETURN A 2 形态启用,投机失败时 RAX = deoptCode,Run 检测后降级调
	// host.Arith 慢路径。
	useSpec bool

	// specDeoptCode 是 PJ2 投机模板 deopt block 烧入的常量(承 04-osr-deopt.md
	// exit reason code)。Run 检测段返 RAX == specDeoptCode 即走慢路径。
	// 选 0xFF...FFFE000(NaN-box 非数字段非任何合法 Lua 值)避免误判。
	specDeoptCode uint64

	// PJ3 FORLOOP reg-limit deopt 路径标志:
	//   - forLoopDeopt = true:本 p4Code 是 PJ3 reg-limit FORLOOP 形态,
	//     Run 端检测 raxSpec==deoptCode 时调 host.ForPrep 而非 host.Arith
	//     (byte-equal 解释器 raise `'for' limit must be a number` 等)
	//   - forLoopA:FORPREP/FORLOOP 的 A 字段(R(A)..R(A+2)= init/limit/step
	//     三槽,host.ForPrep 用本字段定位槽)
	//   - forLoopLimitReg:reg-limit 模板期望的 R(forLoopLimitReg) 槽位号
	//   - forLoopUpvalIdx:upvalue-limit 子形态的 upval idx + 1(0 = 不走
	//     upval;>0 = Run 端先 host.GetUpval(idx-1) + SetReg 写 limit 槽)
	forLoopDeopt    bool
	forLoopA        uint8
	forLoopLimitReg uint8
	forLoopUpvalIdx uint8

	// PJ4 IC ArrayHit 路径标志:
	//   - icArrayHit = true:Run 端检测 raxSpec==deoptCode 时调 host.GetTable
	//     (byte-equal 解释器 IC + 哈希 + __index)
	icArrayHit bool

	// PJ4 IC SETTABLE ArrayHit 路径标志:
	//   - icSetArrayHit = true:Run 端 raxSpec==deoptCode 时调 host.SetTable
	//     (byte-equal 解释器 icSetTable + __newindex)。setter 形态 retB=1
	//     无 R(A) 写。
	icSetArrayHit bool

	// PJ5 CALL void 路径标志(承 docs/design/p4-method-jit/05-system-pipeline.md
	// §4.3 + 06-backends.md §3.5):
	//   - isCallVoid = true:Run prelude 路径调 host.CallBaseline 完成 baseline
	//     CALL(byte-equal P1 doCall 分派);followed by DoReturn 弹帧。
	//   - isCallUpval = true:形态 B(GETUPVAL+CALL+RETURN void),Run 端
	//     prelude 调 host.GetUpval 拿被调函数;false 即形态 A(MOVE+CALL+
	//     RETURN void),Run 端调 host.GetReg 拿被调函数
	//   - callA / callB / callC:CALL A B C 三字段(传给 host.CallBaseline)
	//   - callArgCount:0/1/2 参
	//   - callArg1IsK:1 参形态时 true=LOADK / false=MOVE reg;2 K 参形态恒 true
	//   - callArg1K:1 或 2 K 参形态时第一个 K
	//   - callArg1RegSrc:1 reg 参形态时 MOVE.B 源 reg 号
	//   - callArg2K:2 K 参形态时第二个 K
	//
	// 复用 preludeArg 字段:形态 A 时 = MOVE.B(源 reg);形态 B 时 =
	// GETUPVAL.B(upvalue 索引)
	isCallVoid     bool
	isCallUpval    bool
	callA          uint8
	callB          uint8
	callC          uint8
	callArgCount   uint8
	callMultiRet   uint8 // N=0/1 既有(setter/getter 1 返);N>=2 表 N 返值 getter — Run 端 N 个 MOVE 拷贝
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

	// PJ5 TAILCALL 路径标志(承 docs/design/p4-method-jit/05-system-pipeline.md §4.3):
	//   - isTailCall = true:Run prelude 路径调 host.TailCall 三态分支:
	//     0=Lua 尾完成(本帧已弹,跳过 DoReturn 直接 return 0)
	//     1=ERR
	//     2=host 尾完成(结果在 R(callA)..,Run 落 dead RETURN B=0 to-top 走 DoReturn)
	//   - 复用 isCallUpval / callA / callB / callC / callArgCount / callArg1* / callArg2K
	//     字段(8 子形态 TA0/TB0/TA1K/TB1K/TA1R/TB1R/TA2K/TB2K)
	//   - 复用 preludeArg = MOVE.B(形态 TA*) / GETUPVAL.B(形态 TB*)
	isTailCall bool

	// PJ5 SELF method call inline 路径标志(承
	// docs/design/p4-method-jit/05-system-pipeline.md §4.3 + 09 §9.17):
	//   - isSelfCall = true:Run prelude 路径先调 host.Self 取 method 入 R(callA) +
	//     装 self R(callA+1),然后调 host.CallBaseline / TailCall 完成 byte-equal
	//     P1 doCall 分派。SELF + CALL 与 SELF + TAILCALL 共享本字段,真正的 CALL/
	//     TAILCALL 分支由 preludeOp + isCallVoid / isTailCall 守门。
	//   - selfCallA:SELF.A = method 结果寄存器(同 callA)
	//   - selfMethodRK:SELF.C 字段(RK 方法名常量索引 0-511)
	//   - selfRecvSrcReg + selfRecvIsUpval:recv 来源 — true=upvalue idx / false=reg
	//
	// **与 isCallVoid / isTailCall 的关系**:isSelfCall 是叠加属性 — Run 端
	// switch CALL/TAILCALL case 内额外加一次 host.Self 调用预处理,其它路径
	// 不变。
	isSelfCall      bool
	selfCallA       uint8
	selfMethodRK    uint16
	selfRecvSrcReg  uint8
	selfRecvIsUpval bool

	// PJ5 SELF + CALL spec template 接入(承 §9.10 PJ4 EmitSelfNodeHit 复用):
	//   - useSpecSelfCall = true:SELF 段经 callJITSpec 跑 EmitSelfNodeHit 字节级
	//     模板(IC NodeHit guard + NodeVal store R(A)=method),跳过 host.Self;
	//     失败 raxSpec==specDeoptCode 时降级 host.Self。
	//   - Run 端预处理:先 host.GetReg/GetUpval + SetReg 装 R(callA)=recv(模拟
	//     MOVE/GETUPVAL,因 spec 段从 R(callA) 字节级读取 receiver),然后 callJITSpec;
	//     成功 → method 已 store R(callA),self 已 store R(callA+1);
	//     失败 → R(callA+1) 已被 store recv(P1 SELF case 同款步骤),降级 host.Self
	//     重新覆盖。然后装 args + host.CallBaseline + host.DoReturn。
	//   - 复用 useSpec + specDeoptCode 字段(spec 段 deopt code)。
	useSpecSelfCall bool

	// PJ5 Option B Spike 1 帧建立内联(承 §9.20):
	//   - useFrameInline = true:Run 端走 runSpecSelfCallInline 替代
	//     host.CallBaseline,mmap 段字节级 inline enterLuaFrame + helper call
	//     executeFrom + popCallInfo(承 §9.20 Spike 1 路线)。
	//   - 守门(承 §9.20.4):callee.NumParams=0 + !IsVararg + !NeedsArg +
	//     MaxStack≤32 + caller-callee Proto 编译期已知 + IC NodeHit + FBSelfMono。
	//   - 失败(callee Proto 不满足守门 / archSupportsFrameInline=false / 段
	//     执行 deopt)→ 降级 useSpecSelfCall(SELF 段字节级 + host.CallBaseline)。
	//   - **Spike 1 阶段尚未真接入**:本字段 + Run 端 runSpecSelfCallInline +
	//     Compile 端 compileSpecSelfCallInline 仍是骨架(emit 模板已字节级实装
	//     amd64 120B / arm64 164B);剩 helper call ABI + e2e prove-the-path
	//     留下批工程。
	useFrameInline bool

	// frameInlineResumeOff PJ5 Option B Spike 1 帧建立内联 resume entry 在
	// mmap 段内的字节偏移(承 §9.20.9 (5) Go 端 dispatcher 协议):
	//   - useFrameInline=true 时 compileSpecSelfCall emit 完 BuildVoid0Arg +
	//     ExitHelperRequest 后记录 = len(buf),用于 PopVoid0Arg 段头位置
	//   - dispatcher 处理完 callee 后用 codePageAddr + frameInlineResumeOff
	//     求 resume entry 绝对地址,trampoline 重 CALL 进 mmap 段续跑
	//   - useFrameInline=false 时本字段恒 0(不进入 useFrameInline 分支)
	//
	// Run 入口经 jitCtx.SetCodePageAddr + SetResumeOff 注入(承 §9.20.9 commit-5
	// 真接入同批),编译期固化为 p4Code 字段,Run 期注入 jitCtx。
	frameInlineResumeOff uint32
}

// Proto 反向指针(trampoline 校验)。
func (c *p4Code) Proto() *bytecode.Proto {
	return c.proto
}

// Run 是 crescent→gibbous 跨层入口(P4 PJ7 真接入)。
//
// **调用契约**(承 gibbous_host.go::enterGibbous):
//   - 入参 stack:P3 复用栈([]uint64,len ≥ 1),P4 不读不写——值栈本体经
//     host.SetReg 操作(P3 wazero CallWithStack 1 槽 buffer 协议与 P4 不
//     兼容,gibbous_host.go::gibbousStack 提供的 buffer 不能当真值栈用);
//   - 入参 base:本帧 R0 字节偏移(stackSegByte + base*8),传给 host.DoReturn
//     用于物化 ci.savedPC + nresults 处理(host 内部经 thread.cur.base 算
//     真实槽位,与 base 字节偏移无关);
//   - 返回 status:0=OK / 1=ERR(P4 永不返 2=DEOPT,因 PJ7 无投机 guard)。
//
// **执行流程**:
//  1. callJITFull 跳进 mmap 段(段内只跑 mov rax, value; ret),拿 RAX;
//  2. writeRetA=true(LOADK/LOADBOOL/LOADNIL):经 host.SetReg(retA, RAX)
//     把 mmap 段算出的常量值写进 R(retA);
//  3. preludeOp 非 0(GETUPVAL):经 host helper 取值再 SetReg(retA, val);
//  4. host.DoReturn(base, retPC, retA, retB):按 nresults 移结果到 funcIdx
//     + 弹本帧 CallInfo + 恢复 caller top。
//
// **真接入路径条件**:hostState != nil(由 wireP4 经 *Compiler.SetHostState
// 注入)。host==nil 仅在 jit 包内 prove-the-path 单测路径(单测不构造完整
// *State,只验值正确路径走到)。bridge 主路径的 wireP4 必经 SetHostState
// 注入,Run 必经 host.DoReturn 弹帧。
//
// **PJ7 真接入子集**(承 analyzeShape):
//   - 长度 1 RETURN A B(空函数 / identity 形态)
//   - LOADK/LOADBOOL/LOADNIL + RETURN A 2(常量返,writeRetA=true)
//   - 首条 RETURN A 2(luac 优化形态:identity 函数)
//   - MOVE A B + RETURN A 2(retA=B 跳过中转)
//   - GETUPVAL A B + RETURN A 2(preludeOp=GETUPVAL)
//   - ADD/SUB/MUL/DIV/MOD/POW A B C + RETURN A 2(preludeOp=算术 op,Run
//     调 host.Arith helper,可 ERR 冒泡)
//   - 长度 3 luac 主 chunk 尾部冗余(LOADK + RETURN + RETURN dead)
func (c *p4Code) Run(stack []uint64, base uint32) int32 {
	if c.codePage == nil || c.jitCtx == nil {
		stack[0] = 1
		return 1
	}

	// **PJ2 完整接入预备**:Run 入口算 arena base + valueStackBase 装入
	// jitContext,让 PJ2+ 字节级算术 codegen 可经 r15+offset 读这两字段
	// (mmap 段直接寻址值栈槽,跳过 host helper round-trip)。当前 PJ7
	// 简化形态 mmap 段是 dummy(mov+ret 不读 r15),装值不被使用——但
	// 装值本身正确无负作用,落实 PJ2 完整接入路径的 Go 端起点(承
	// 05 §5 arena base 重载协议:每次 Run 入口现算不缓存,grow 安全)。
	var vsBaseAddr uintptr
	if c.host != nil && c.jitCtx != nil {
		c.jitCtx.SetArenaBase(c.host.ArenaBaseAddr())
		vsBaseAddr = c.host.ValueStackBaseAddr(int32(base))
		c.jitCtx.SetValueStackBase(vsBaseAddr)
		// PJ5 Option B Spike 1+ 帧建立内联(承 §9.20):Run 入口现算注入
		// ciDepth / ciSegBase / top 镜像字 host 字节地址(arena grow 后地址变,
		// 不缓存——同 ArenaBase 重载协议)。
		c.jitCtx.SetCIDepthAddr(c.host.CIDepthHostAddr())
		c.jitCtx.SetCISegBaseAddr(c.host.CISegBaseHostAddr())
		c.jitCtx.SetTopAddr(c.host.TopHostAddr())
	}

	jitCtxAddr := jitContextAddr(c.jitCtx)

	// PJ2 投机模板路径(useSpec=true 时,ADD A B C 形态走 callJITSpec + deopt
	// 检测;失败时降级调 host.Arith 慢路径)
	//
	// **mock host 兜底**:host.ArenaBaseAddr 返 0(单测 mock 无真 arena)时
	// 跳过 spec 路径直接走 host helper——避免段段读 [rbx+0] = 读 0 地址 SIGSEGV。
	if c.useSpec && c.host != nil && vsBaseAddr != 0 {
		// **PJ5 SELF + CALL spec template 独立路径**(承 §9.10 EmitSelfNodeHit 复用 +
		// §9.17 升级):SELF 段走字节级模板(跳过 host.Self)+ CALL 段走 host.CallBaseline。
		// 自包含子路径——不与下方 PJ2/PJ3/PJ4 spec 分流混淆。
		if c.useSpecSelfCall {
			return c.runSpecSelfCall(int32(base), jitCtxAddr, vsBaseAddr)
		}
		// **upvalue-limit 预处理**:reg-limit 模板期望 R(forLoopLimitReg)
		// 是 number。upval 形态 Run 端先调 host.GetUpval(idx-1) + SetReg
		// 写 limit 槽,然后模板字节级 inline 走 reg-limit 路径(IsNumber
		// guard 仍生效——若 upval 非 number,guard 失败 → host.ForPrep raise
		// byte-equal P1)。
		if c.forLoopUpvalIdx > 0 {
			upvalVal := c.host.GetUpval(int32(base), int32(c.forLoopUpvalIdx-1))
			c.host.SetReg(int32(c.forLoopLimitReg), upvalVal)
		}
		raxSpec := archCallJITSpec(c.codePage.Addr(), jitCtxAddr, vsBaseAddr)
		if raxSpec == c.specDeoptCode {
			// **PJ4 IC ArrayHit deopt** 路径:调 host.GetTable byte-equal P1
			// (经 IC + 哈希 + __index 元方法链;preludeOp/Arg/C 已存
			// GETTABLE 信息)
			if c.icArrayHit {
				specPC := int32(c.retPC) - 1
				st := c.host.GetTable(int32(base), specPC, int32(c.retA),
					int32(c.preludeArg), int32(c.preludeC))
				if st != 0 {
					return st
				}
				_ = stack
				c.host.DoReturn(int32(base), int32(c.retPC), int32(c.retA), int32(c.retB))
				return 0
			}

			// **PJ4 IC SETTABLE ArrayHit deopt** 路径:调 host.SetTable
			// byte-equal P1(经 icSetTable + __newindex 元方法链)。setter 形态
			// retB=1 无 R(A) 写,DoReturn 不读 R(A)。
			if c.icSetArrayHit {
				specPC := int32(c.retPC) - 1
				st := c.host.SetTable(int32(base), specPC, int32(c.retA),
					int32(c.preludeArg), int32(c.preludeC))
				if st != 0 {
					return st
				}
				_ = stack
				c.host.DoReturn(int32(base), int32(c.retPC), int32(c.retA), int32(c.retB))
				return 0
			}

			// **PJ3 FORLOOP reg-limit deopt** 路径分流:调 host.ForPrep
			// 而非 host.Arith(byte-equal 解释器 raise non-number 错误)
			if c.forLoopDeopt {
				st := c.host.ForPrep(int32(base), int32(c.retPC)-2 /* FORPREP pc=3 */, int32(c.forLoopA))
				if st != 0 {
					return st
				}
				// host.ForPrep 已置三槽 number + 预减,但 P4 不接 host.ForLoop
				// (不实装段内迭代器跑剩余循环 — 那需要完整 doForLoop helper)。
				// 简化:host.ForPrep 成功后直接 DoReturn 返(等同 P4 帧只跑
				// FORPREP coercion,实际循环在 P1 解释器 — TODO 完整 host.ForLoop)。
				// **此简化致 reg-limit FORLOOP 在 deopt 路径上 byte-equal 但
				// 性能等价 P1**(deopt 后不再加速)— 与设计 04 §5 deopt
				// 协议一致(投机失败即降级解释器)。
				_ = stack
				c.host.DoReturn(int32(base), int32(c.retPC), int32(c.retA), int32(c.retB))
				return 0
			}
			// chain 形态 preludePC 算法:op1 真实 pc = retPC - 2(普通单 op
			// retPC = 1 + chain retPC = 2 两种)
			specPC := int32(c.retPC) - 1
			if c.chainOp != 0 {
				specPC = int32(c.retPC) - 2
			}
			// 投机失败 → 降级调 host.Arith 慢路径(byte-equal 解释器)
			st := c.host.Arith(int32(base), specPC, int32(c.preludeOp),
				int32(c.preludeArg), int32(c.preludeC), int32(c.retA))
			if st != 0 {
				return st
			}
			// **chain spec deopt 路径**:chain 模板 deopt 时只跑了 op1
			// (host.Arith 上一句),还要跑 op2 才与解释器 byte-equal。
			// chainB 在 analyzeArithChainForm 已固定 = retA(中间值衔接),
			// op2 实际 pc = specPC + 1(op1 之后一位)。
			if c.chainOp != 0 {
				st2 := c.host.Arith(int32(base), specPC+1,
					int32(c.chainOp), int32(c.chainB), int32(c.chainC),
					int32(c.retA))
				if st2 != 0 {
					return st2
				}
			}
		}
		// OK 路径(快路径或 deopt 慢路径都已写 R(retA))
		_ = stack
		c.host.DoReturn(int32(base), int32(c.retPC), int32(c.retA), int32(c.retB))
		return 0
	}

	rax := archCallJITFull(c.codePage.Addr(), jitCtxAddr)

	// 写 R(retA) = RAX(仅当 writeRetA=true 且 retB ≥ 2)。
	if c.writeRetA && c.retB >= 2 && c.host != nil {
		c.host.SetReg(int32(c.retA), rax)
	}

	// PJ7 prelude opcode 处理(GETUPVAL / ADD-POW / UNM / LEN / NEWTABLE /
	// GETTABLE 等 host 调用形态):mmap 段不调 host(避免完整 trampoline 复杂
	// 度),改在 Go 端 Run 内调。
	//
	// **pc 实参规约**(承 gibbous_host.go::Arith/Unm/Len/NewTable/GetTable
	// 各 helper):pc 是「**被执行 opcode 自身的 index**」——helper 内
	// `ci.pc = pc + 1` 复刻解释器主循环「取指后 ci.pc 已 ++」纪律,与
	// errWithName 的 `ci.pc-1==pc(失败 op)` / annotateError 的
	// `LineInfo[ci.pc-1]` / IC 槽 `proto.IC[pc]` 三处下游对齐。
	//
	// **二段链式 chain 形态**(retPC=2,op1 在 pc 0,op2 在 pc 1):
	// 慢路径 op1 实参 pc = 0,op2 实参 pc = 1。chain 形态时 preludePC
	// 算法 retPC - 1 = 1 失准(对位 op2 位置而非 op1)——chain 形态
	// op1 真实 pc = retPC - 2 = 0。
	//
	// **普通单 op 形态**(retPC=1,prelude 在 pc 0):preludePC = retPC - 1 = 0。
	preludePC := int32(c.retPC) - 1
	if c.chainOp != 0 {
		// chain 形态 retPC=2,op1 真实 pc = retPC - 2 = 0
		preludePC = int32(c.retPC) - 2
	}
	// **retB 守卫拆分**:之前 `c.retB >= 2` 统一守卫只适合 getter 形态
	// (R(A) 写返回值),setter 形态(SETTABLE/SETGLOBAL 0 返回值)retB=1
	// 也需走 prelude。各 case 按需自查 retB(getter 检 >=2,setter 不检)。
	if c.preludeOp != 0 && c.host != nil {
		switch c.preludeOp {
		case uint8(bytecode.GETUPVAL):
			if c.retB < 2 {
				break
			}
			val := c.host.GetUpval(int32(base), int32(c.preludeArg))
			c.host.SetReg(int32(c.retA), val)
		case uint8(bytecode.GETGLOBAL):
			if c.retB < 2 {
				break
			}
			// 全局读:host.DoGetGlobal(base, pc, a, bx)——经 icGetTable 走
			// IC + 哈希 + __index 元方法链(`_G` 表),可 raise。
			// 注:bx(Bx 字段,18-bit)经 preludeArg(uint32)透传。
			st := c.host.DoGetGlobal(int32(base), preludePC, int32(c.retA),
				int32(c.preludeArg))
			if st != 0 {
				return st
			}
		case uint8(bytecode.ADD), uint8(bytecode.SUB), uint8(bytecode.MUL),
			uint8(bytecode.DIV), uint8(bytecode.MOD), uint8(bytecode.POW):
			if c.retB < 2 {
				break
			}
			// 算术族:调 host.Arith 慢路径 helper(逐字节同构解释器 doArith)。
			// helper 内用 reg/RK 取 B/C + setReg 写 R(A) + ci.pc=pc+1 物化
			// 失败 op 索引(用于 errWithName 行号 + IC 反馈槽对齐)。
			st := c.host.Arith(int32(base), preludePC, int32(c.preludeOp),
				int32(c.preludeArg), int32(c.preludeC), int32(c.retA))
			if st != 0 {
				// raise pending:host 端已置 gibbousPendingErr,enterGibbous
				// 取走冒泡(不调 DoReturn 弹帧,ERR 路径不经 RETURN)。
				return st
			}
			// 二段算术链式形态(MUL+ADD+RETURN 等):调第二次 host.Arith,
			// pc=preludePC+1(chainOp 在 op1 之后)。chainB 已是 retA(链式
			// 输入,已在 op1 写入 R(retA))。
			if c.chainOp != 0 {
				st2 := c.host.Arith(int32(base), preludePC+1, int32(c.chainOp),
					int32(c.chainB), int32(c.chainC), int32(c.retA))
				if st2 != 0 {
					return st2
				}
			}
		case uint8(bytecode.UNM):
			if c.retB < 2 {
				break
			}
			// 一元负号:host.Unm 逐字节同构解释器 UNM 慢路径(string coercion
			// + __unm 元方法,可 raise)。
			st := c.host.Unm(int32(base), preludePC,
				int32(c.preludeArg), int32(c.retA))
			if st != 0 {
				return st
			}
		case uint8(bytecode.LEN):
			if c.retB < 2 {
				break
			}
			// 长度运算:host.Len 逐字节同构解释器 LEN(string 字节长 / table
			// border / table __len / 异类报错,可 raise)。
			st := c.host.Len(int32(base), preludePC,
				int32(c.preludeArg), int32(c.retA))
			if st != 0 {
				return st
			}
		case uint8(bytecode.NEWTABLE):
			if c.retB < 2 {
				break
			}
			// 建表:host.NewTable(base, pc, a, b, c)——alloc + safepoint 全
			// helper 内,B/C 是 Fb 编码的初始数组/哈希段大小提示。永不 raise
			// (Go runtime OOM 才崩,与本错误协议解耦)。
			c.host.NewTable(int32(base), preludePC, int32(c.retA),
				int32(c.preludeArg), int32(c.preludeC))
		case uint8(bytecode.GETTABLE):
			if c.retB < 2 {
				break
			}
			// 表读:host.GetTable(base, pc, a, b, c)——经 IC + 哈希 + __index
			// 元方法链,可 raise(attempt to index nil/string with key/...)。
			// 注:pc 也用作 IC 槽索引(proto.IC[pc])——必须 preludePC 才能
			// 命中 GETTABLE 的私有 IC 槽,而非 RETURN 的空槽(语义正确性)。
			st := c.host.GetTable(int32(base), preludePC, int32(c.retA),
				int32(c.preludeArg), int32(c.preludeC))
			if st != 0 {
				return st
			}
		case uint8(bytecode.SETTABLE):
			// 表写:host.SetTable(base, pc, a, b, c)——经 IC + 哈希 +
			// __newindex 元方法链,可 raise。**setter 形态 retB=1(0 返回
			// 值)**,不写 R(A),不需要 retB 守卫。
			st := c.host.SetTable(int32(base), preludePC, int32(c.retA),
				int32(c.preludeArg), int32(c.preludeC))
			if st != 0 {
				return st
			}
		case uint8(bytecode.SETGLOBAL):
			// 全局写:host.DoSetGlobal(base, pc, a, bx)——经 icSetTable 走
			// IC + `_G` 表 + __newindex,可 raise。setter 形态。
			st := c.host.DoSetGlobal(int32(base), preludePC, int32(c.retA),
				int32(c.preludeArg))
			if st != 0 {
				return st
			}
		case uint8(bytecode.SETUPVAL):
			// upvalue 写:host.SetUpvalFromReg(base, a, b)——读 R(a) + upvalSet
			// 写 upvalue。永不 raise。setter 形态。
			c.host.SetUpvalFromReg(int32(base), int32(c.retA), int32(c.preludeArg))
		case uint8(bytecode.NOT):
			if c.retB < 2 {
				break
			}
			// 逻辑非:pure Truthy(无 metamethod、无 raise)。Run 直接经
			// host.GetReg 读 R(B) + SetReg(A, BoolValue(!Truthy(...)))。
			v := value.Value(c.host.GetReg(int32(c.preludeArg)))
			c.host.SetReg(int32(c.retA), uint64(value.BoolValue(!value.Truthy(v))))
		case uint8(bytecode.CALL):
			// PJ5 CALL void 形态:`function(g) g() end` (形态 A,isCallUpval=false)
			// 或 `local function noop()...end; function() noop() end`(形态 B,
			// isCallUpval=true)— MOVE+CALL+RETURN void / GETUPVAL+CALL+RETURN void
			// (0 参形态,callArgCount=0)或 MOVE/GETUPVAL+LOADK+CALL+RETURN void
			// (1 参 K 形态,callArgCount=1)。
			//
			// **pc 实参**:CALL 自身 pc 由 retPC-1 算(0 参形态 retPC=2,1 参形态
			// retPC=3,CALL 都在 RETURN 前一条)。
			//
			// **预处理 — 把被调函数装到 R(callA) 槽**:luac 编 MOVE/GETUPVAL
			// 在 mmap 段是 dummy 不执行,Run 端手动调 host helper 完成。
			//   - 形态 A:host.GetReg(preludeArg=MOVE.B) + SetReg(callA)
			//   - 形态 B:host.GetUpval(base, preludeArg=GETUPVAL.B) + SetReg(callA)
			//
			// **1 参形态 LOADK 装载**:callArgCount=1 时,Run 端 host.SetReg(callA+1,
			// callArg1K)把编译期烧入的 K 常量装到参数槽。
			//
			// **SELF inline 形态**(isSelfCall=true):recv 装 R(callA) 后,先调
			// host.Self 完成 R(callA)=R(callA)[K_method] + R(callA+1)=self;
			// 然后参数装到 R(callA+2) 起(跳过 self 槽)— byte-equal 解释器
			// SELF + CALL inline 子集。
			callPC := int32(c.retPC) - 1
			var srcVal uint64
			if c.isCallUpval {
				srcVal = c.host.GetUpval(int32(base), int32(c.preludeArg))
			} else {
				srcVal = c.host.GetReg(int32(c.preludeArg))
			}
			c.host.SetReg(int32(c.callA), srcVal)
			// SELF inline 预处理:host.Self 完成 method 取值 + self 装载
			if c.isSelfCall {
				// pc 实参:SELF 自身 pc。CALL 在 retPC-1,SELF 在 CALL 之前一条 + args
				// (callArgCount 条 LOADK/MOVE)。即 SELF.pc = callPC - 1 - callArgCount。
				selfPC := callPC - 1 - int32(c.callArgCount)
				st := c.host.Self(int32(base), selfPC, int32(c.selfCallA),
					int32(c.selfCallA), int32(c.selfMethodRK))
				if st != 0 {
					return st
				}
			}
			argOffset := int32(1)
			if c.isSelfCall {
				argOffset = 2 // self 占 R(callA+1),args 从 R(callA+2)
			}
			c.loadCallArgs(argOffset)
			// baseline doCall:绕过 R3 indirect 哨兵(本简化形态不支持段内
			// call_indirect),host/crescent/__call/gibbous 全形态同步跑完。
			st := c.host.CallBaseline(int32(base), callPC,
				int32(c.callA), int32(c.callB), int32(c.callC))
			if st != 0 {
				return st
			}
			// N>=2 返值 getter 形态:Run 端做 N 个 MOVE 拷贝以保留 byte-equal
			// (luac 编 R(callA+nret+k) ← R(callA+k),然后 RETURN A=callA+nret B=nret+1)。
			// 末尾 DoReturn 用 retA/retB 已设好(retA=callA+nret,retB=nret+1)读 R(callA+nret..)
			// 拷到 caller 槽。
			if c.callMultiRet >= 2 {
				nret := int32(c.callMultiRet)
				for k := int32(0); k < nret; k++ {
					c.host.SetReg(int32(c.callA)+nret+k,
						c.host.GetReg(int32(c.callA)+k))
				}
			}
		case uint8(bytecode.TAILCALL):
			// PJ5 TAILCALL 形态(承 docs/design/p4-method-jit/05-system-pipeline.md
			// §4.3 + analyzeTailCallForm):`function() return f() end` 类
			// 单 CallExpr 作 return 唯一表达式被 luac 翻成 TAILCALL + 尾随
			// dead RETURN B=0(to-top)+ 隐式 RETURN B=1。
			//
			// **pc 实参**:TAILCALL 自身 pc = retPC-1(retPC 指 dead RETURN)。
			//
			// **预处理 — 把被调函数装到 R(callA) 槽**(与 CALL void 同款,装
			// 完后调 host.TailCall 完成尾调用):
			//   - 形态 TA*:host.GetReg(MOVE.B) + SetReg(callA)
			//   - 形态 TB*:host.GetUpval(base, GETUPVAL.B) + SetReg(callA)
			//
			// **SELF inline 形态**(isSelfCall=true):同 CALL 路径,recv 装
			// R(callA) 后调 host.Self,args 从 R(callA+2) 起;然后调 host.TailCall
			// 完成尾调用三态分支。
			//
			// **三态分支**(crescent.State.TailCall + jit/host.go::TailCall 同款):
			//   - 0 = Lua 尾完成:caller 帧已被 callee 帧替换 + executeFrom
			//     同步驱动 callee 链到完成 + nresults 写回 funcIdx。Run 端
			//     **跳过 DoReturn**(本帧已弹),直接 return 0(本函数末尾
			//     的 DoReturn 调用须被 isTailCall 守卫跳过)。
			//   - 1 = ERR:raise pending → 上层 ERR 冒泡。
			//   - 2 = host 尾完成:结果已落 R(callA..),G 帧未弹。Run 端**正常
			//     调 DoReturn**(对位 dead RETURN A=callA B=0 to-top,nret =
			//     top - (base + callA),DoReturn 内 B=0 多值路径)。
			tailPC := int32(c.retPC) - 1
			var srcVal uint64
			if c.isCallUpval {
				srcVal = c.host.GetUpval(int32(base), int32(c.preludeArg))
			} else {
				srcVal = c.host.GetReg(int32(c.preludeArg))
			}
			c.host.SetReg(int32(c.callA), srcVal)
			// SELF inline 预处理:host.Self 完成 method 取值 + self 装载
			if c.isSelfCall {
				selfPC := tailPC - 1 - int32(c.callArgCount)
				st := c.host.Self(int32(base), selfPC, int32(c.selfCallA),
					int32(c.selfCallA), int32(c.selfMethodRK))
				if st != 0 {
					return st
				}
			}
			argOffset := int32(1)
			if c.isSelfCall {
				argOffset = 2
			}
			c.loadCallArgs(argOffset)
			st := c.host.TailCall(int32(base), tailPC,
				int32(c.callA), int32(c.callB), int32(c.callC))
			switch st {
			case 0:
				// Lua 尾完成:本帧已被 callee 帧替换 + executeFrom 同步驱动
				// callee 链到完成。直接 return 0,跳过末尾 DoReturn(本帧已弹)。
				_ = stack
				return 0
			case 1:
				return 1
			case 2:
				// host 尾完成:结果在 R(callA..),G 帧未弹。fall through 到末尾
				// DoReturn(retB=0 多值 to-top 路径)。
			default:
				// 未来扩展:本接口当前只定义 0/1/2 三态。其它值视为 ERR 兜底。
				return 1
			}
		case uint8(bytecode.EQ), uint8(bytecode.LT), uint8(bytecode.LE):
			if c.retB < 2 {
				break
			}
			// 比较折叠形态:6-op 模板 EQ/LT/LE + JMP + LOADBOOL × 2 + RETURN
			// (+ dead RETURN) 等价 `R(retA) = BoolValue(cmp == (cmpA==1))`。
			// host.Compare 返 packed:bit0=结果 / bit1=错误标志。
			//
			// **pc 实参**:此形态 retPC=4(RETURN 在 pc 4),preludePC=3 是
			// 错——比较 op 实际在 pc 0。直接传 0 用作 preludePC(行号/IC
			// 槽锚定到比较 op 自身)。
			packed := c.host.Compare(int32(base), 0, int32(c.preludeOp),
				int32(c.preludeArg), int32(c.preludeC))
			if packed&2 != 0 {
				// bit1 = ERR pending(raiseGibbous 已置)
				return 1
			}
			cmpResult := packed & 1 // bit0
			// 折叠:结果与 cmpA 匹配则 true(luac 的 LOADBOOL 序列等价)。
			boolVal := value.False
			if cmpResult == int32(c.cmpA) {
				boolVal = value.True
			}
			c.host.SetReg(int32(c.retA), uint64(boolVal))
		}
	}

	_ = stack // P3 协议参数,P4 不读不写

	if c.host != nil {
		c.host.DoReturn(int32(base), int32(c.retPC), int32(c.retA), int32(c.retB))
	}

	return 0
}

// PendingErr 默认返 nil(P4 PJ2 简化形态不持错误状态——Run 直接返 status)。
func (c *p4Code) PendingErr() error {
	return nil
}

// runSpecSelfCall 处理 PJ5 SELF + CALL spec template 路径(承
// compileSpecSelfCall + §9.10 EmitSelfNodeHit 复用)。
//
// **流程**:
//  1. 先装 R(callA) = recv(模拟 luac MOVE/GETUPVAL,因 spec 段从 R(callA)
//     字节级读 receiver)。
//  2. callJITSpec 跑 EmitSelfNodeHit 模板:
//     - 成功 → R(callA) = method(已 store)+ R(callA+1) = self(已 store)
//     - 失败(raxSpec == specDeoptCode)→ 降级 host.Self(R(callA+1) 已被
//     模板 store recv,P1 SELF case 同款步骤;host.Self 重新覆盖 byte-equal)
//  3. callArgCount=0(本批仅 0 参形态),无 args 装载。
//  4. host.CallBaseline 完成 CALL 段。
//  5. host.DoReturn 弹帧。
//
// byte-equal P1:成功路径 = 字节级 NodeHit 直达槽(跳过哈希),与 P1 icGetTable
// NodeHit 命中结果一致;失败路径 = host.Self 完整 P1 SELF 段。
func (c *p4Code) runSpecSelfCall(base int32, jitCtxAddr uintptr, vsBaseAddr uintptr) int32 {
	// 1. 装 R(callA) = recv(模拟 MOVE/GETUPVAL)
	// MOVE form(form M*):recv 装载已字节级 emit 在 spec 段头(承 §9.19 args
	// inline 同款摊薄),跳过 host.GetReg+SetReg 2 跨界。
	// GETUPVAL form(form U*):upvalue 不在 vsBase 栈,仍 Run 端 host 装载。
	if c.selfRecvIsUpval {
		upvalVal := c.host.GetUpval(base, int32(c.selfRecvSrcReg))
		c.host.SetReg(int32(c.callA), upvalVal)
	}

	// 2. callJITSpec 跑 SELF 段
	raxSpec := archCallJITSpec(c.codePage.Addr(), jitCtxAddr, vsBaseAddr)
	if raxSpec == c.specDeoptCode {
		// 失败 deopt:SELF NodeHit guard 不成立(table shape 变 / key 退化 /
		// NodeVal=nil)= 真正的投机失败 → OSR exit。接 p4SpecState 计数(承
		// §9.18 + 04 §5.1 单次失败三件事:计数 +1,达阈值切 P4Deoptimized)。
		onOSRExit(c.proto)
		// 降级 host.Self(byte-equal P1 SELF 段)
		// SELF 自身 pc = callPC - 1(CALL 在 retPC-1,SELF 在 CALL 前一条,0 参形态)
		callPC := int32(c.retPC) - 1
		selfPC := callPC - 1 - int32(c.callArgCount)
		st := c.host.Self(base, selfPC, int32(c.selfCallA),
			int32(c.selfCallA), int32(c.selfMethodRK))
		if st != 0 {
			return st
		}
	} else if c.useFrameInline && raxSpec == uint64(ExitInlineHelper) {
		// **§9.20.9 Run-end dispatcher 实装**(commit-5b):spec 段 emit 完
		// SELF NodeHit + BuildVoid0Arg + ExitHelperRequest 后段返 RAX=
		// ExitInlineHelper,Run 端走 dispatcher 路径完成 callee Lua 体执行,
		// 然后二次 callJITSpec 跳 resume entry(codePage + frameInlineResumeOff)
		// 续跑 PopVoid0Arg + ret 完成 popCallInfo。
		//
		// 流程(对位 §9.20.9 (1) 协议总览,Run 端化实装):
		//   2a. dispatchInlineHelper(jitCtx) → 路由 helper request(经
		//       jitCtx.exitArg0 = HelperRunCallee)→ host.ExecuteCalleeFromInlineFrame
		//       (readCISegInto + nCcalls++ + executeFrom + popCallInfo)
		//   2b. 若 dispatcher 返 1=ERR:错误冒泡,直接 return 1
		//   2c. 二次 archCallJITSpec 跳 resume entry,RAX 必 = 0(PopVoid0Arg
		//       + ret 段执行完返 ExitNormal=0)
		//   2d. 跳过 Run 端 host.CallBaseline + DoReturn(callee 已 inline 跑完)
		st := c.runFrameInlineDispatcher(base)
		if st != 0 {
			return st
		}
		return 0
	}

	// 3. args 装载已在 spec 段字节级 emit(承 §9.19 摊薄优化,跳过 host
	// round-trip)。spec 段执行后 args 已落 R(callA+2..callA+1+N)。
	// **deopt 路径下** spec 段中途返回 deoptCode,args 装载段(在 SELF 之前)
	// 已执行完,R(callA+2..) 已装好——降级 host.Self 后 args 仍可用,无需重装。

	// 4. CALL 段 / TAILCALL 段(byte-equal P1)
	callPC := int32(c.retPC) - 1
	if c.isTailCall {
		// TAILCALL 三态分支(承 host.TailCall 同款语义):
		//   0 = Lua 尾完成(本帧已弹,跳过 DoReturn 直接 return)
		//   1 = ERR
		//   2 = host 尾完成(结果在 R(callA..),G 帧未弹,落 DoReturn dead RETURN B=0)
		st := c.host.TailCall(base, callPC, int32(c.callA), int32(c.callB), int32(c.callC))
		switch st {
		case 0:
			return 0
		case 1:
			return 1
		case 2:
			// fall through to DoReturn
		default:
			return 1
		}
	} else {
		// CALL void:byte-equal P1 doCall
		st := c.host.CallBaseline(base, callPC, int32(c.callA), int32(c.callB), int32(c.callC))
		if st != 0 {
			return st
		}
	}

	// 5. DoReturn 弹帧(setter 形态 retB=1,0 返值)
	c.host.DoReturn(base, int32(c.retPC), int32(c.retA), int32(c.retB))
	return 0
}

// loadCallArgs 把 callArg1..7 装到 R(callA+offset), R(callA+offset+1), ...
// offset = 1(普通 CALL/TAILCALL,args 从 R(callA+1));2(SELF inline,
// args 从 R(callA+2),因 R(callA+1)=self)。
func (c *p4Code) loadCallArgs(offset int32) {
	if c.callArgCount >= 1 {
		var argVal uint64
		if c.callArg1IsK {
			argVal = c.callArg1K
		} else {
			argVal = c.host.GetReg(int32(c.callArg1RegSrc))
		}
		c.host.SetReg(int32(c.callA)+offset+0, argVal)
	}
	if c.callArgCount >= 2 {
		var argVal uint64
		if c.callArg2IsK {
			argVal = c.callArg2K
		} else {
			argVal = c.host.GetReg(int32(c.callArg2RegSrc))
		}
		c.host.SetReg(int32(c.callA)+offset+1, argVal)
	}
	if c.callArgCount >= 3 {
		var argVal uint64
		if c.callArg3IsK {
			argVal = c.callArg3K
		} else {
			argVal = c.host.GetReg(int32(c.callArg3RegSrc))
		}
		c.host.SetReg(int32(c.callA)+offset+2, argVal)
	}
	if c.callArgCount >= 4 {
		var argVal uint64
		if c.callArg4IsK {
			argVal = c.callArg4K
		} else {
			argVal = c.host.GetReg(int32(c.callArg4RegSrc))
		}
		c.host.SetReg(int32(c.callA)+offset+3, argVal)
	}
	if c.callArgCount >= 5 {
		var argVal uint64
		if c.callArg5IsK {
			argVal = c.callArg5K
		} else {
			argVal = c.host.GetReg(int32(c.callArg5RegSrc))
		}
		c.host.SetReg(int32(c.callA)+offset+4, argVal)
	}
	if c.callArgCount >= 6 {
		var argVal uint64
		if c.callArg6IsK {
			argVal = c.callArg6K
		} else {
			argVal = c.host.GetReg(int32(c.callArg6RegSrc))
		}
		c.host.SetReg(int32(c.callA)+offset+5, argVal)
	}
	if c.callArgCount >= 7 {
		var argVal uint64
		if c.callArg7IsK {
			argVal = c.callArg7K
		} else {
			argVal = c.host.GetReg(int32(c.callArg7RegSrc))
		}
		c.host.SetReg(int32(c.callA)+offset+6, argVal)
	}
}

// Slot 返回共享 funcref 表槽号 + 是否登记。
//
// **P4 原生码无 wasm 表概念**——恒返 (0, false),让上层走同步 Run fallback
// (gibbous→gibbous 调用走 P4 自家直跳协议,而非经 wasm `call_indirect`)。
// 承 `p2-bridge/05-p3-p4-interface.md` §6 GibbousCode.Slot 注:「P4 原生码
// 永走回退」。
func (c *p4Code) Slot() (uint32, bool) {
	return 0, false
}

// Dispose 释放 mmap 段(幂等)。
//
// **关键纪律**(承 05 §2.1.3):Dispose 触发 munmap 必须保证「该段所有调用
// 者已退出」——若有 goroutine 正在该段执行,munmap 等于 UAF。在 P2 状态机
// 里 Dispose 触发点是「升层失败/降层」时刻,该段此刻没有活跃调用(状态转换
// 前已经 quiesce)。多 State 并发场景留 PJ7 验收期落地(可能解法:引用计数
// + 延迟 munmap)。
func (c *p4Code) Dispose() error {
	if c.codePage != nil {
		err := c.codePage.Munmap()
		c.codePage = nil
		return err
	}
	return nil
}

// ErrRunNotImplemented:占位错误(已被 PJ2 真接入版淘汰,但保留作 wireP4
// 防御性兜底返错的错误类型——若 codePage 构造失败 Run 直接返 ERR)。
var ErrRunNotImplemented = errors.New("internal/gibbous/jit: p4Code Run failed: codePage / jitCtx not initialized")

// runFrameInlineDispatcher 处理 PJ5 Option B Spike 1 帧建立内联 Run-end
// dispatcher 路径(承 §9.20.9 (1)+(5) Go 端 dispatcher 实装,commit-5b
// Run-end 化版本):
//
// **协议**:spec 段已 emit 完 SELF NodeHit + BuildVoid0Arg + ExitHelperRequest
// + (resume entry) PopVoid0Arg + ret;段返 RAX=ExitInlineHelper 时本函数
// 接管完成 callee Lua 体执行 + popCallInfo 重入。
//
// 流程:
//  1. 读 jitCtx.exitArg0 路由 helper request(承 §9.20.9 (3) 协议状态码):
//     - HelperRunCallee:调 host.ExecuteCalleeFromInlineFrame(base, retA)
//     完成 readCISegInto + nCcalls++ + executeFrom + popCallInfo
//     - HelperGrowStack:未来扩(arena grow 触发)
//     - HelperGCBarrier:未来扩(GC 写屏障)
//  2. 若 helper 返 1=ERR,设 ERR 返 1(错误冒泡)
//  3. 二次 archCallJITSpec 跳 codePage + frameInlineResumeOff 续跑 PopVoid0Arg
//     + ret 段,RAX 必 = 0(ExitNormal)
//  4. 跳过 Run 端 host.CallBaseline + DoReturn(callee 已在 helper 内完整跑完)
//
// **设计澄清**(承 commit-3b 跨包 CALL 设计澄清的延续):
//   - dispatcher 路由本应是 dispatchInlineHelper(jit 包级函数),但跨包 + Plan 9
//     asm 复杂度高;改用 p4Code 方法直接访问 c.host(承 Run-end 化方案)
//   - dispatchInlineHelper 留作工程基础锚点,future 真 trampoline asm CALL 路径
//     的接口(承 §9.20.9 (5))
//
// **当前 archSupportsFrameInline=false 屏蔽真触发**,本函数不被调到;
// commit-5e 翻闸门 + analyzeSelfCallSpecForm 设 useFrameInline=true 后启用。
func (c *p4Code) runFrameInlineDispatcher(base int32) int32 {
	// 1. 路由 helper request:读 jitCtx.exitArg0 决定 helper 类型
	helperCode := c.jitCtx.ExitArg0()
	switch helperCode {
	case HelperRunCallee:
		// 跑 callee Lua 体(host 完成 readCISegInto + executeFrom + popCallInfo)
		retA := int32(c.retA) // callee 返值落 R(retA..) 区间
		st := c.host.ExecuteCalleeFromInlineFrame(base, retA)
		if st != 0 {
			// 错误冒泡(host 内 raise 已置 pendingErr)
			return 1
		}
	case HelperGrowStack, HelperGCBarrier:
		// 未来扩,当前不触达(spec 段不 emit 这些 request)
		return 1
	default:
		// 未知 helper code(协议 bug)
		return 1
	}
	// 2. 二次 callJITSpec 跳 resume entry 续跑 PopVoid0Arg + ret
	resumeAddr := c.codePage.Addr() + uintptr(c.frameInlineResumeOff)
	jitCtxAddr := jitContextAddr(c.jitCtx)
	vsBaseAddr := c.host.ValueStackBaseAddr(base)
	raxResume := archCallJITSpec(resumeAddr, jitCtxAddr, vsBaseAddr)
	if raxResume != 0 {
		// resume entry 段执行异常(理论上 PopVoid0Arg + ret 只 ciDepth-- + ret,
		// RAX 应是 ExitNormal=0;非 0 说明协议 bug)
		return 1
	}
	return 0
}

// 编译期断言:Compiler 实现 bridge.P3Compiler 接口;p4Code 实现 bridge.GibbousCode
// (任何接口签名漂移立即在编译期暴露,不等运行期)。
var (
	_ bridge.P3Compiler  = (*Compiler)(nil)
	_ bridge.GibbousCode = (*p4Code)(nil)
)
