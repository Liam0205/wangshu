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
	}

	jitCtxAddr := jitContextAddr(c.jitCtx)

	// PJ2 投机模板路径(useSpec=true 时,ADD A B C 形态走 callJITSpec + deopt
	// 检测;失败时降级调 host.Arith 慢路径)
	//
	// **mock host 兜底**:host.ArenaBaseAddr 返 0(单测 mock 无真 arena)时
	// 跳过 spec 路径直接走 host helper——避免段段读 [rbx+0] = 读 0 地址 SIGSEGV。
	if c.useSpec && c.host != nil && vsBaseAddr != 0 {
		raxSpec := archCallJITSpec(c.codePage.Addr(), jitCtxAddr, vsBaseAddr)
		if raxSpec == c.specDeoptCode {
			// 投机失败 → 降级调 host.Arith 慢路径(byte-equal 解释器)
			st := c.host.Arith(int32(base), int32(c.retPC)-1, int32(c.preludeOp),
				int32(c.preludeArg), int32(c.preludeC), int32(c.retA))
			if st != 0 {
				return st
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
	// PJ7 simple-form prelude 恒在 pc 0(retPC 恒为 1,见 analyzeShape),
	// 故传 `preludePC = retPC-1 = 0`。
	// **DoReturn 的 pc 实参另算**:DoReturn 处理的就是 RETURN 自身,helper
	// 内不再 `+1`,直接传 retPC 是正确的——别一起改错。
	preludePC := int32(c.retPC) - 1
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

// 编译期断言:Compiler 实现 bridge.P3Compiler 接口;p4Code 实现 bridge.GibbousCode
// (任何接口签名漂移立即在编译期暴露,不等运行期)。
var (
	_ bridge.P3Compiler  = (*Compiler)(nil)
	_ bridge.GibbousCode = (*p4Code)(nil)
)
