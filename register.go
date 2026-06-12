// HostFn 注册与模块化暴露——把 Go 函数挂进 Lua 全局/模块表,供 Lua 端调用。
// 设计形态:11 §10 列表风格的 P1 版本(不是 §10.1 栈机风格 HostCtx)。
package wangshu

import (
	"github.com/Liam0205/wangshu/internal/crescent"
	"github.com/Liam0205/wangshu/internal/value"
)

// HostFn 是注册到 Lua 全局/模块表后由 Lua 端调用的 Go 函数签名(11 §10
// 的 P1 列表风格版本——不是 §10.1 的栈机风格 HostCtx,见 godoc 备注)。
//
// st 为 fn 所属 State(单 goroutine 约定见 11 §8)。args 为 Lua 端传入的
// 实参,顺序与脚本调用一致。返回 results 是 Lua 端接收的多返回值;返回
// error 时 Lua 端可由 pcall/xpcall 捕获(err.Error() → 错误消息)。
//
// 当前范围:args 中的 table/function/userdata 仍映射为 Nil(fromInner 收紧,
// 本期面只暴露 function 的 GetGlobal/Call 闭环,不暴露 host fn 接收的 Lua
// function);标量(nil/bool/number/string)往返正常。
type HostFn = func(*State, []Value) ([]Value, error)

// wrapHostFn 把公共 HostFn 包装为 internal crescent.HostFn(签名形态对齐)。
func (st *State) wrapHostFn(fn HostFn) crescent.HostFn {
	return func(_ *crescent.State, iargs []value.Value) ([]value.Value, *crescent.LuaError) {
		args := make([]Value, len(iargs))
		for i, a := range iargs {
			args[i] = fromInner(st, a)
		}
		results, err := fn(st, args)
		if err != nil {
			return nil, crescent.NewError(err.Error())
		}
		out := make([]value.Value, len(results))
		for i, v := range results {
			out[i] = v.toInner(st)
		}
		return out, nil
	}
}

// Register 把 Go 函数 fn 注册为全局名 name 的 Lua 可调用函数(对标
// gopher-lua `L.SetGlobal(name, L.NewFunction(fn))` 与 `L.Register`)。
//
// 调用形态:Lua 端 `name(args...)` 触发 fn(st, args),返回值即 Lua 端接收
// 的多返回值;err != nil 时 Lua 端经 pcall/xpcall 捕获。
//
// host closure 的 Go 侧直接 Call(state.Call(fn, ...))本轮未开,只能从
// Lua 内调用。
func (st *State) Register(name string, fn HostFn) {
	id := st.core.RegisterHostFn(st.wrapHostFn(fn))
	cl := st.core.MakeHostClosure(id)
	st.core.SetGlobal(name, value.MakeGC(value.TagFunction, cl))
}

// RegisterModule 把一组 Go 函数注册为一个全局表 name(对标 gopher-lua
// `L.SetGlobal(name, L.SetFuncs(L.NewTable(), funcs))` 的简化)。
//
// 用法:Lua 端 `name.fname(args...)` 触发对应 fn;表本身只在创建瞬间填好,
// Lua 端动态写入 / 覆盖此表的能力沿用普通 Lua 表语义(无只读保护)。
func (st *State) RegisterModule(name string, fns map[string]HostFn) {
	tbl := st.core.NewLibTable(uint32(len(fns)))
	for fname, fn := range fns {
		id := st.core.RegisterHostFn(st.wrapHostFn(fn))
		cl := st.core.MakeHostClosure(id)
		st.core.SetTableField(tbl, fname, value.MakeGC(value.TagFunction, cl))
	}
	st.core.SetGlobal(name, value.MakeGC(value.TagTable, tbl))
}
