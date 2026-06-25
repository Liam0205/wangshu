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
	jitCtx *JITContext

	// retA 是 RETURN 指令的 A 寄存器号——p4Code.Run 的 mmap 段返回 RAX 后,
	// 把 RAX 写到 stack[base/8 + retA] 槽位(= R(retA) NaN-box)。
	retA uint8

	// retB 是 RETURN 指令的 B 字段(B-1 = 返回值个数)。
	//   - retB = 1:0 个返回值(空 RETURN);Run 不写 stack 槽
	//   - retB = 2:1 个返回值;Run 把 RAX 写到 stack[base/8 + retA]
	retB uint8

	// retPC 是 RETURN 指令的 pc(从 0 起)——DoReturn 用于物化 ci.savedPC。
	//   - 长度 1 形态(纯 RETURN):retPC = 0
	//   - 长度 2 形态(LOADK/LOADBOOL/LOADNIL + RETURN):retPC = 1
	retPC uint8

	// host 是注入的 P4HostState(从 *Compiler 拷贝):per-p4Code 持有,无并发
	// write(只在 Compile 时写一次,Run 时只读)——V18 -race 友好。
	host P4HostState
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
//  4. **PJ7 真接入**:调 hostState.DoReturn 完成「按 nresults 移结果到
//     funcIdx + 弹帧 + 恢复 caller top」——这让 enterGibbous 后栈状态
//     等同解释器跑完该帧(承 gibbous_host.go::enterGibbous 调用契约);
//  5. 返 0(OK)。
//
// **PJ7 真接入路径条件**:hostState != nil(由 crescent.State.wireP4 经
// SetP4HostState 注入)。若 hostState == nil 则跳过 step 4,Run 仍写值但
// 不弹帧——这是 PJ2 内部 prove-the-path 单测的形态(单测不构造完整 *State,
// 只验值正确)。
//
// 入参:
//   - stack:复用栈([]uint64),len ≥ 1;
//   - base:本帧 R0 在共见 linear memory 的字节偏移(= stackSegByte +
//     base*8);P4 PJ2 简化形态下 stack 起点 = arena Go 堆 backing,base 经
//     base/8 即 R0 的 uint64 槽下标。
//
// 返回 status:0=OK / 1=ERR(P4 永不返 2=DEOPT,因 PJ2 不引入投机 guard)。
func (c *p4Code) Run(stack []uint64, base uint32) int32 {
	if c.codePage == nil || c.jitCtx == nil {
		stack[0] = 1
		return 1
	}

	jitCtxAddr := jitContextAddr(c.jitCtx)
	rax := jitamd64.CallJITFull(c.codePage.Addr(), jitCtxAddr)

	// 写 R(retA) = RAX(仅当返回值个数 >= 1,即 retB >= 2)。
	// retB = 1 时 0 个返回值,不写 stack。
	if c.retB >= 2 {
		stack[uint32(c.retA)+base/8] = rax
	}
	_ = rax // 0 返回值时 RAX 是 mmap 段 mov 进的 dummy(我们仍发了 mov+ret)

	if c.host != nil {
		c.host.DoReturn(int32(base), int32(c.retPC) /*retPC*/, int32(c.retA) /*A*/, int32(c.retB) /*B*/)
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
