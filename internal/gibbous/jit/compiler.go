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
//   - LOADK A K(Bx);RETURN A 1(常量返回,K(Bx) 非字符串占位)
//   - LOADBOOL A B 0;RETURN A 1(bool 返回,C=0 不跳)
//   - LOADNIL A A;RETURN A 1(单 nil 返回,A==B)
//
// **关键**:三档共同点是「编译期能算出 R(A) 的最终 NaN-box u64 值」——
// 这是 P4 PJ7 简化形态的根本约束(mmap 段只发 mov rax, imm64; ret)。
//
// PJ8+ 启动时扩 supported(MOVE 寄存器拷 + ADD/SUB 算术 + 等)需要 jitContext
// load/store 值栈,留下一阶段。
func (c *Compiler) SupportsAllOpcodes(proto *bytecode.Proto) bool {
	_, _, ok := analyzeShape(proto)
	return ok
}

// analyzeShape 识别支持的「单值产生 + RETURN A 1」形态,返 (retA, value, ok)。
//
// ok=true 时 retA 是 RETURN 的 A 寄存器号,value 是 R(retA) 的最终 NaN-box
// u64 值(由首条 opcode 编译期决定)。
//
// 支持形态:
//
//   - 长度 1:RETURN A 1 (B=1,返回 0 个值,即 `function() end`/`return`)
//   - 长度 2:LOADK/LOADBOOL/LOADNIL A ... + RETURN A 2(返回 1 个值)
func analyzeShape(proto *bytecode.Proto) (uint8, uint64, bool) {
	if proto == nil {
		return 0, 0, false
	}

	// 形态 0:长度 1,RETURN A B=1(返回 0 个值,空函数)
	if len(proto.Code) == 1 {
		ret := proto.Code[0]
		if bytecode.Op(ret) != bytecode.RETURN {
			return 0, 0, false
		}
		retB := bytecode.B(ret)
		if retB != 1 { // B=1 即 B-1=0 个返回值
			return 0, 0, false
		}
		// 0 返回值无需写值——但本 PJ7 简化形态 mmap 段恒发 mov rax, imm; ret,
		// 不会写值栈(p4Code.Run 仍调 DoReturn 完成弹帧;DoReturn nret = b-1 = 0
		// 即不移结果,直接弹帧)。retA 任意,value 任意 (沿用 0)。
		return 0, 0, true
	}

	if len(proto.Code) != 2 {
		return 0, 0, false
	}
	// 第二条必须是 RETURN A 2(返回 1 个值)
	ret := proto.Code[1]
	if bytecode.Op(ret) != bytecode.RETURN {
		return 0, 0, false
	}
	retA := bytecode.A(ret)
	retB := bytecode.B(ret)
	if retB != 2 {
		return 0, 0, false
	}

	first := proto.Code[0]
	switch bytecode.Op(first) {
	case bytecode.LOADK:
		loadA := bytecode.A(first)
		loadBx := bytecode.Bx(first)
		if loadA != retA {
			return 0, 0, false
		}
		if loadBx < 0 || loadBx >= len(proto.Consts) {
			return 0, 0, false
		}
		if proto.IsStringConst(loadBx) {
			return 0, 0, false
		}
		return uint8(retA), uint64(proto.Consts[loadBx]), true

	case bytecode.LOADBOOL:
		loadA := bytecode.A(first)
		loadB := bytecode.B(first)
		loadC := bytecode.C(first)
		if loadA != retA {
			return 0, 0, false
		}
		if loadC != 0 {
			return 0, 0, false
		}
		var v value.Value
		if loadB != 0 {
			v = value.BoolValue(true)
		} else {
			v = value.BoolValue(false)
		}
		return uint8(retA), uint64(v), true

	case bytecode.LOADNIL:
		loadA := bytecode.A(first)
		loadB := bytecode.B(first)
		if loadA != retA || loadA != loadB {
			return 0, 0, false
		}
		return uint8(retA), uint64(value.Nil), true
	}
	return 0, 0, false
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
	retA, val, ok := analyzeShape(proto)
	if !ok {
		return nil, ErrCompileUnsupportedShape
	}

	// 取 retB(从最后一条 RETURN 指令读)
	retInsn := proto.Code[len(proto.Code)-1]
	retB := uint8(bytecode.B(retInsn))

	// 发射:mov rax, val; ret(emitter 内已在 PJ1 实装)。
	// 即使 retB=1(0 返回值),仍发 mov+ret——mmap 段不能为空,RAX dummy 值
	// 不会被写入栈(retB=1 时 p4Code.Run 不写 stack)。
	var buf []byte
	buf = jitamd64.EmitMovRaxImm64(buf, val)
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
		retA:     retA,
		retB:     retB,
		retPC:    uint8(len(proto.Code) - 1), // RETURN 是最后一条指令
		host:     c.hostState,
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
