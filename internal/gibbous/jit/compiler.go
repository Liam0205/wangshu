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
	preludeOp  uint8  // RETURN 前 prelude opcode(0=无,GETUPVAL=4 等)
	preludeArg uint8  // prelude opcode 的 B 字段
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
			preludeArg: uint8(guvB),
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
			if proto.IsStringConst(loadBx) {
				return shapeInfo{}
			}
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
		host:       c.hostState,
	}, nil
}

// ErrCompileUnsupportedShape:Compile 拒绝 Proto 形态不在 PJ7 真接入子集的
// 兜底返错——SupportsAllOpcodes 已在 F7 拦下绝大多数,本错误是 PJ2 内部
// prove-the-path 单测路径绕过 SupportsAllOpcodes 直调 Compile 时的二次形态
// 检查兜底。bridge 收到本错误把该 Proto 标 TierStuck(永久解释,不重试)。
var ErrCompileUnsupportedShape = errors.New("internal/gibbous/jit: P4 PJ7 only supports single-BB shape (LOADK / LOADBOOL / LOADNIL + RETURN A 1 / RETURN A 1)")
