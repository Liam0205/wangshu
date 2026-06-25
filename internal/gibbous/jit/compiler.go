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
// **PJ7 真接入实装**:开放白名单到「LOADK A K(0); RETURN A 1」单 BB 形态——
// 这是 spike 闸门 ⊕ trampoline ⊕ emitter 三件套唯一无副作用、无 helper、
// 无跨层调用的 Lua 子集。
//
// 形态约束(必须同时满足):
//   - len(Code) == 2
//   - Code[0] = LOADK
//   - Code[1] = RETURN
//   - LOADK A == RETURN A(共享寄存器号)
//   - RETURN B == 2(返回 1 个值)
//   - LOADK Bx 指向常量池非字符串占位(Compile 期再校验,因 SupportsAllOpcodes
//     不读 Consts)
//
// 任何不满足的 Proto 返 false ⇒ F7 闸门拦下 ⇒ 走 crescent 解释。
//
// **关键**:本函数只校验形态轮廓,Compile 期再做严格校验(包含字符串占位
// 检查)。两层校验:SupportsAllOpcodes 是 hot path 启发式 + Compile 是
// 真发射前最终把关。
//
// PJ8+ 启动时扩 supported(MOVE/LOADBOOL/LOADNIL/JMP → ADD/SUB/...)。
func (c *Compiler) SupportsAllOpcodes(proto *bytecode.Proto) bool {
	if proto == nil || len(proto.Code) != 2 {
		return false
	}
	loadk := proto.Code[0]
	ret := proto.Code[1]
	if bytecode.Op(loadk) != bytecode.LOADK {
		return false
	}
	if bytecode.Op(ret) != bytecode.RETURN {
		return false
	}
	if bytecode.A(loadk) != bytecode.A(ret) {
		return false
	}
	if bytecode.B(ret) != 2 {
		return false
	}
	// LOADK Bx 范围 + 字符串占位检查(防御性,Compile 也会再查)
	loadBx := bytecode.Bx(loadk)
	if loadBx < 0 || loadBx >= len(proto.Consts) {
		return false
	}
	if proto.IsStringConst(loadBx) {
		return false
	}
	return true
}

// Compile 把 Proto 编译成 GibbousCode(可执行产物)。
//
// **PJ2 真接入实装**:识别「LOADK A K(0); RETURN A 1」单 BB 形态——
//  1. 取 K(0) 的 NaN-box uint64 值(常量池第一项);
//  2. emitter 发射 `mov rax, NaNBoxedConst; ret`(11 字节);
//  3. mmap PROT_RW + 写码 + mprotect PROT_RX(承 05 §2.1);
//  4. 包装 *p4Code(retA = LOADK 与 RETURN 共享的 A)。
//
// 其它形态返 ErrCompileUnsupportedShape(承
// `p2-bridge/05-p3-p4-interface.md` §2.2.2 错误返回语义)——bridge 收到错误
// 后把该 Proto 标 TierStuck(永久解释,不重试)。
//
// **PJ2 阶段 SupportsAllOpcodes 仍全 false** ⇒ 本路径不被 bridge 在主库
// 主路径走到;仅 PJ2 内部 prove-the-path 单测会调本函数(承 prove-the-path-under-test
// 纪律,白盒证明 mmap 段被走到 + 值正确)。
func (c *Compiler) Compile(proto *bytecode.Proto, feedback *bridge.TypeFeedback) (bridge.GibbousCode, error) {
	_ = feedback
	if proto == nil {
		return nil, ErrCompileUnsupportedShape
	}

	// 识别「LOADK A K(0); RETURN A 1」最简单 BB 形态。
	if len(proto.Code) != 2 {
		return nil, ErrCompileUnsupportedShape
	}
	loadk := proto.Code[0]
	ret := proto.Code[1]
	if bytecode.Op(loadk) != bytecode.LOADK {
		return nil, ErrCompileUnsupportedShape
	}
	if bytecode.Op(ret) != bytecode.RETURN {
		return nil, ErrCompileUnsupportedShape
	}
	loadA := bytecode.A(loadk)
	loadBx := bytecode.Bx(loadk)
	retA := bytecode.A(ret)
	retB := bytecode.B(ret)
	// 形态约束:LOADK 与 RETURN 共享 A;RETURN 返回 1 个值(B=2)。
	if loadA != retA || retB != 2 {
		return nil, ErrCompileUnsupportedShape
	}
	// 常量必须存在 + 是非 String 占位(PJ2 不处理字符串 intern)。
	if loadBx < 0 || loadBx >= len(proto.Consts) {
		return nil, ErrCompileUnsupportedShape
	}
	if proto.IsStringConst(loadBx) {
		// 字符串常量需要 State 私有 intern,不在 PJ2 简化形态内(留 PJ4+)。
		return nil, ErrCompileUnsupportedShape
	}
	constVal := uint64(proto.Consts[loadBx])

	// 发射:mov rax, constVal; ret(emitter 内已在 PJ1 实装)。
	var buf []byte
	buf = jitamd64.EmitMovRaxImm64(buf, constVal)
	buf = jitamd64.EmitRet(buf)

	// W^X 翻面 + mmap。
	page, err := jitamd64.MmapCode(buf)
	if err != nil {
		return nil, err
	}

	return &p4Code{
		proto:    proto,
		codePage: page,
		jitCtx:   NewJITContext(),
		retA:     uint8(retA),
		host:     c.hostState, // per-Compiler hostState 拷给 p4Code,避免 global race
	}, nil
}

// ErrCompileNotImplemented:PJ0 占位错误——P4 后端尚未实装(已被 PJ2 真接入
// 版淘汰,但保留作 PJ2 范围外形态的兜底兼容)。
var ErrCompileNotImplemented = errors.New("internal/gibbous/jit: PJ0 skeleton — Compile not implemented")

// ErrCompileUnsupportedShape:PJ2 阶段 Compile 拒非「LOADK A K(0); RETURN A 1」
// 形态——SupportsAllOpcodes 全 false 已在 F7 拦下绝大多数情况;本错误是
// PJ2 内部 prove-the-path 单测路径绕过 SupportsAllOpcodes 直调 Compile 时
// 的形态检查兜底。
var ErrCompileUnsupportedShape = errors.New("internal/gibbous/jit: PJ2 only supports LOADK + RETURN single-BB shape")
