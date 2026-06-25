//go:build wangshu_p4

package jit

import (
	"errors"

	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
	jitamd64 "github.com/Liam0205/wangshu/internal/gibbous/jit/amd64"
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
	codePage *jitamd64.CodePage

	// jitCtx 是本编译产物的 JIT 执行上下文(per-Proto 单例)。
	//
	// **PJ2 阶段**:JITContext 字段大多数 0(arenaBase / valueStackBase 等
	// 在 PJ3+ 模板需要时填实);本字段引用为了让 callJITFull 拿到 r15 装载值
	// 不为 nil(防 SEGV)。
	jitCtx *JITContext

	// retA 是 RETURN 指令的 A 寄存器号——p4Code.Run 的 mmap 段返回 RAX 后,
	// 把 RAX 写到 stack[base/8 + retA] 槽位(= R(retA) NaN-box)。
	//
	// **PJ2 阶段固化**:仅「LOADK A K(0); RETURN A 1」单一形态(retA = LOADK
	// 与 RETURN 共享的 A);PJ3+ 多 BB / 多 RETURN 形态时本字段升级为 PC →
	// 寄存器映射表(承 04-osr-deopt §4.1 编译期 PC 映射)。
	retA uint8
}

// Proto 反向指针(trampoline 校验)。
func (c *p4Code) Proto() *bytecode.Proto {
	return c.proto
}

// Run 是 crescent→gibbous 跨层入口。
//
// **PJ2 真接入实装**(承 jitamd64.CallJITFull):
//  1. 经 callJITFull 跳进 mmap 段(段内只跑 mov rax, NaNBoxedConst; ret);
//  2. 拿到 RAX = NaN-box const;
//  3. 写到 stack[base/8 + retA] 槽位(= R(retA));
//  4. stack[0] = 0(status OK);
//  5. 返 0。
//
// **PJ2 简化形态契约**:不弹 CallInfo 帧(留调用方 enterGibbous 路径处理);
// 不调 host helper(模板内不需);不写 ci.savedPC(LOADK+RETURN 单 BB,
// pc 物化由调用方做)。
//
// 这与 P3 *p3Code.Run 的契约不同——P3 通过 wasm 内 `h_return` helper 完成
// 「写值 + 弹帧」(承 04-trampoline §4.7);P4 PJ2 简化形态选「Go 端拆帧」,
// 即 enterGibbous 类调用方在 Run 返回后自己经 DoReturn 等价路径弹帧。
//
// 接入 crescent end-to-end 留 PJ3+(需 enterGibbousJIT 路径在 crescent 端
// 实装 + SupportsAllOpcodes 开 LOADK/RETURN 白名单 + 配套 -race / difftest
// 验证)。本 PJ2 阶段 SupportsAllOpcodes 仍全 false ⇒ 本路径不被 doCall 走到,
// p4Code 不会被实例化——本 Run 实装是 PJ3+ 启动时的物理基础。
//
// 入参:
//   - stack:复用栈([]uint64),len ≥ 1;stack[0] 入参 = base 字节偏移,
//     返回后 stack[0] = status(0=OK / 1=ERR / 2=DEOPT);
//   - base:本帧 R0 在共见 linear memory 的字节偏移(= stackSegByte +
//     base*8);P4 PJ2 简化形态下 stack 起点 = arena Go 堆 backing,base 经
//     base/8 即 R0 的 uint64 槽下标。
//
// 返回 status:0=OK / 1=ERR(P4 永不返 2=DEOPT,因 PJ2 不引入投机 guard)。
func (c *p4Code) Run(stack []uint64, base uint32) int32 {
	// PJ2 防御性检查:codePage / jitCtx 必须已构造(由 Compile 设置)。
	if c.codePage == nil || c.jitCtx == nil {
		stack[0] = 1 // ERR
		return 1
	}

	// 经 callJITFull 跳进 mmap 段(完整 trampoline:保存 callee-saved + 装
	// r15 = jitCtx)。段内跑 `mov rax, NaNBoxedConst; ret`,RAX 拿回。
	//
	// jitCtx 经 unsafe.Pointer → uintptr 传(承 trampoline_full_linux_amd64.go
	// 入参契约):Go 堆对象不移动,uintptr 稳定。
	jitCtxAddr := jitContextAddr(c.jitCtx)
	rax := jitamd64.CallJITFull(c.codePage.Addr(), jitCtxAddr)

	// 写 R(retA) = RAX(LOADK A K(0) 的语义:R(A) = K(0),retA = A)。
	//
	// stack 是值栈整体(arena `[]uint64` backing 的 Go 视图);base 是字节偏移,
	// 转成 uint64 下标 = base/8。R(retA) 槽位 = stack[base/8 + retA]。
	//
	// **PJ2 简化形态边界**:retA 是 uint8(LOADK 编码 A 是 8 bit),base+retA*8
	// 不会越界——stack 由 enterGibbous 的 ensureStack 保证 ≥ MaxStack。
	stack[uint32(c.retA)+base/8] = rax

	// status = 0(OK)。stack[0] 在 P3 的 CallWithStack 协议里返回,P4 简化
	// 形态下 stack[0] 仍存 base(因为 stack 是复用的,base 已被 caller 用过);
	// 但 enterGibbous 调用方读的是 Run 的返回值 int32 而非 stack[0],所以
	// 实际不依赖 stack[0] 写回。
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
