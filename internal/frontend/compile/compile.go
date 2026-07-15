// Package compile turns an AST into a bytecode.Proto with Lua 5.1-compatible
// register allocation (04 §1 soft commitments; §5-§9 the main body).
//
// Compile is the package's top-level entry point:
//
//	proto, protos, err := compile.Compile(ast.Block, sourceName)
//
// proto is the main chunk's *bytecode.Proto; protos is every sub-Proto in ProtoID
// order (with proto itself appended at the end). A sub-Proto is referenced inside
// proto.Protos by its index in the protos slice (corresponding to the Bx field of
// 02 §4 CLOSURE).
package compile

import (
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/frontend/ast"
)

// Compile turns the parsed AST chunk into the main Proto plus its full
// nested Proto registry. The main chunk is equivalent to a vararg function body (Lua 5.1).
func Compile(block *ast.Block, source string) (mainID uint32, protos []*bytecode.Proto, err error) {
	defer func() {
		if r := recover(); r != nil {
			if ce, ok := r.(*CompileError); ok {
				err = ce
				return
			}
			panic(r)
		}
	}()
	cg := &codegen{source: source}
	mainExpr := &ast.FuncExpr{
		Line:       1,
		Params:     nil,
		IsVararg:   true,
		NoArgTable: true, // main chunk has no implicit arg table (official VARARG_ISVARARG only)
		Body:       block,
	}
	cg.compileFunc(nil, mainExpr)
	mainID = uint32(len(cg.protos) - 1)
	return mainID, cg.protos, nil
}

// compileFunc compiles a single FuncExpr, appends the result to cg.protos, and returns that Proto.
//
// outerFS == nil means the main chunk (no enclosing function); otherwise it is used
// for resolving upvalues along the chain.
func (cg *codegen) compileFunc(outerFS *funcState, fe *ast.FuncExpr) *bytecode.Proto {
	fs := newFuncState(cg, outerFS, cg.source, fe.Line)
	fs.proto.NumParams = uint8(len(fe.Params))
	fs.proto.IsVararg = fe.IsVararg
	fs.proto.NeedsArg = fe.IsVararg && !fe.NoArgTable // LUA_COMPAT_VARARG implicit arg table
	fs.proto.LineEnd = fe.EndLine
	fs.isVararg = fe.IsVararg

	// parameters are registered as locals in R(0..NumParams-1)
	for _, p := range fe.Params {
		fs.registerLocal(fe.Line, p)
	}
	// the implicit arg table takes the first register after the parameters (the official
	// "arg" local; the interpreter fills the table on frame entry)
	if fs.proto.NeedsArg {
		fs.registerLocal(fe.Line, "arg")
	}
	fs.freereg = fs.nactvar
	if int(fs.proto.MaxStack) < fs.freereg {
		fs.proto.MaxStack = uint8(fs.freereg)
	}

	fs.block(fe.Body)

	// always emit a trailing implicit RETURN (matching Lua 5.1 close_func: an
	// unconditional luaK_ret(0,0)).
	fs.emitABC(fe.EndLine, bytecode.RETURN, 0, 1, 0)

	// close the locals still alive
	fs.removeVars(0)

	// backfill the local-variable debug table (01 §5.7 LocVars; used by 09 §8.4 error-name inference)
	fs.proto.LocVars = make([]bytecode.LocalVar, len(fs.locvars))
	for i, lv := range fs.locvars {
		fs.proto.LocVars[i] = bytecode.LocalVar{
			Name:    lv.name,
			StartPC: lv.startPC,
			EndPC:   lv.endPC,
		}
	}

	// MaxStack is at least 2 (Lua 5.1 LUA_MINSTACK headroom; a call needs room for at least the function + 1 arg, 04 §5.3)
	if fs.proto.MaxStack < 2 {
		fs.proto.MaxStack = 2
	}

	cg.protos = append(cg.protos, fs.proto)
	// P2 PB7 wiring: under a profile build, run compilability analysis and write the
	// result into Proto.Compilability + Proto.CompReasons (03 §6.3 wiring + 02 §2.4
	// AST use-and-discard scheme ①). Under a !wangshu_profile build this is a no-op,
	// and the Proto fields keep their zero values.
	//
	// **PJ5 extension (2026-06-27)**: the WithOuter path passes in outerFS so that
	// AnalyzeProto can see localFnAsts on the enclosing funcState chain (per 03 §9
	// GAP-5 scope-aware name resolution) — this lets a closure's call to an
	// enclosing local known fn be recognized as a known call rather than an unknown
	// call, opening the promotion path for the P4 PJ5 GETUPVAL+CALL+RETURN void case.
	analyzeCompilabilityWithOuter(fe, fs.proto, outerFS)
	return fs.proto
}
