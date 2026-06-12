// Package compile turns an AST into a bytecode.Proto with Lua 5.1-compatible
// register allocation (04 §1 软承诺;§5-§9 主体)。
//
// Compile 是包顶层入口:
//
//	proto, protos, err := compile.Compile(ast.Block, sourceName)
//
// proto 是主 chunk 的 *bytecode.Proto,protos 是按 ProtoID 顺序的全部子 Proto(含
// proto 自身在末尾)。子 Proto 在 proto.Protos 中以 protos 切片下标引用(对应 02 §4
// CLOSURE 的 Bx 字段)。
package compile

import (
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/frontend/ast"
)

// Compile turns the parsed AST chunk into the main Proto plus its full
// nested Proto registry. main chunk 等价于 vararg 函数体(Lua 5.1)。
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
		Line:     1,
		Params:   nil,
		IsVararg: true,
		Body:     block,
	}
	cg.compileFunc(nil, mainExpr)
	mainID = uint32(len(cg.protos) - 1)
	return mainID, cg.protos, nil
}

// compileFunc 编译一个 FuncExpr,把产物加入 cg.protos 并返回该 Proto。
//
// outerFS 为 nil 表示主 chunk(无外层函数);否则用于 upvalue 沿链解析。
func (cg *codegen) compileFunc(outerFS *funcState, fe *ast.FuncExpr) *bytecode.Proto {
	fs := newFuncState(cg, outerFS, cg.source, fe.Line)
	fs.proto.NumParams = uint8(len(fe.Params))
	fs.proto.IsVararg = fe.IsVararg
	fs.proto.LineEnd = fe.EndLine
	fs.isVararg = fe.IsVararg

	// 形参注册为 R(0..NumParams-1) 上的局部
	for _, p := range fe.Params {
		fs.registerLocal(fe.Line, p)
	}
	fs.freereg = fs.nactvar
	if int(fs.proto.MaxStack) < fs.freereg {
		fs.proto.MaxStack = uint8(fs.freereg)
	}

	fs.block(fe.Body)

	// 始终发尾部隐式 RETURN(对齐 Lua 5.1 close_func:无条件 luaK_ret(0,0))。
	fs.emitABC(fe.EndLine, bytecode.RETURN, 0, 1, 0)

	// 关闭尚活的局部
	fs.removeVars(0)

	// 回填局部变量调试表(01 §5.7 LocVars;09 §8.4 错误名字推断用)
	fs.proto.LocVars = make([]bytecode.LocalVar, len(fs.locvars))
	for i, lv := range fs.locvars {
		fs.proto.LocVars[i] = bytecode.LocalVar{
			Name:    lv.name,
			StartPC: lv.startPC,
			EndPC:   lv.endPC,
		}
	}

	// MaxStack 至少 2(Lua 5.1 LUA_MINSTACK 余量;调用至少需放函数+1参,04 §5.3)
	if fs.proto.MaxStack < 2 {
		fs.proto.MaxStack = 2
	}

	cg.protos = append(cg.protos, fs.proto)
	return fs.proto
}
