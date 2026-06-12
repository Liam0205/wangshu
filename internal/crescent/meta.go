// Metatable support — __index / __newindex 链 + 算术 metamethod(07 的 M11 最小集)。
//
// metatable 经 object.TableMetaRef 存取(arena 原生 Table 布局 word4);
// 完整 arena 哈希接入时迁回 object.TableMetaRef。
package crescent

import (
	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// metaOf 返回 t 的 metatable GCRef(无则 0)。
func (st *State) metaOf(t arena.GCRef) arena.GCRef {
	return object.TableMetaRef(st.arena, t)
}

// SetMeta 设置 t 的 metatable(0 = 清除)。object.SetTableMeta 内部 BumpGen。
//
// 同步解析 metatable.__mode 写入弱表缓存位(07 §13:GC 不在 mark 阶段解析
// 字符串,setmetatable 是唯一写入口)。
func (st *State) SetMeta(t, meta arena.GCRef) {
	object.SetTableMeta(st.arena, t, meta)
	weakKey, weakVal := false, false
	if meta != 0 {
		mode := st.metaField(t, "__mode")
		if value.Tag(mode) == value.TagString {
			for _, c := range object.StringBytes(st.arena, value.GCRefOf(mode)) {
				if c == 'k' {
					weakKey = true
				}
				if c == 'v' {
					weakVal = true
				}
			}
		}
	}
	object.SetTableWeakFlags(st.arena, t, weakKey, weakVal)
}

// metaField 查 t 的 metatable[name];无 metatable 或无该域返回 Nil。
func (st *State) metaField(t arena.GCRef, name string) value.Value {
	mt := st.metaOf(t)
	if mt == 0 {
		return value.Nil
	}
	key := value.MakeGC(value.TagString, st.gc.Intern([]byte(name)))
	v, _ := st.tableGet(mt, key)
	return v
}

// metaFieldOfValue 对任意 Value 查元方法(M11 仅支持 table;string 等 per-type
// 元表留 P1 后续)。
func (st *State) metaFieldOfValue(v value.Value, name string) value.Value {
	if value.Tag(v) == value.TagTable {
		return st.metaField(value.GCRefOf(v), name)
	}
	return value.Nil
}

// indexWithMeta 实现 GETTABLE 的完整语义:raw get → __index 链(07 §3)。
//
// 链上限 100 层(防 __index 环)。string 值的 __index = string 库表(07 §1.2)。
func (st *State) indexWithMeta(th *thread, obj, key value.Value) (value.Value, *LuaError) {
	for depth := 0; depth < 100; depth++ {
		if value.Tag(obj) == value.TagTable {
			tref := value.GCRefOf(obj)
			v, e := st.tableGet(tref, key)
			if e != nil {
				return value.Nil, e
			}
			if v != value.Nil {
				return v, nil
			}
			h := st.metaField(tref, "__index")
			if h == value.Nil {
				return value.Nil, nil // raw miss,无 __index
			}
			if value.Tag(h) == value.TagFunction {
				return st.callMetaHandler(th, h, []value.Value{obj, key}, 1)
			}
			obj = h // __index 是表:沿链重查
			continue
		}
		if value.Tag(obj) == value.TagString && st.stringLib != 0 {
			// string 的 per-type __index = string 库(`("x"):upper()`)
			obj = value.MakeGC(value.TagTable, st.stringLib)
			continue
		}
		return value.Nil, errf("attempt to index a %s value", typeName(obj))
	}
	return value.Nil, errf("'__index' chain too long; possible loop")
}

// setIndexWithMeta 实现 SETTABLE 的完整语义:raw set → __newindex 链(07 §4)。
func (st *State) setIndexWithMeta(th *thread, obj, key, val value.Value) *LuaError {
	for depth := 0; depth < 100; depth++ {
		if value.Tag(obj) == value.TagTable {
			tref := value.GCRefOf(obj)
			v, e := st.tableGet(tref, key)
			if e != nil {
				return e
			}
			if v != value.Nil {
				// 已存在键:直接 raw set,不触发 __newindex
				return st.tableSet(tref, key, val)
			}
			h := st.metaField(tref, "__newindex")
			if h == value.Nil {
				return st.tableSet(tref, key, val)
			}
			if value.Tag(h) == value.TagFunction {
				_, e := st.callMetaHandler(th, h, []value.Value{obj, key, val}, 0)
				return e
			}
			obj = h
			continue
		}
		return errf("attempt to index a %s value", typeName(obj))
	}
	return errf("'__newindex' chain too long; possible loop")
}

// arithMeta 算术慢路径:b/c 任一带 __add 等元方法时调用(07 §5)。
//
// name 形如 "__add"。返回结果值。
func (st *State) arithMeta(th *thread, name string, b, c value.Value) (value.Value, *LuaError) {
	h := st.metaFieldOfValue(b, name)
	if h == value.Nil {
		h = st.metaFieldOfValue(c, name)
	}
	if h == value.Nil {
		bad := b
		if value.IsNumber(b) {
			bad = c
		}
		return value.Nil, errf("attempt to perform arithmetic on a %s value", typeName(bad))
	}
	if value.Tag(h) != value.TagFunction {
		return value.Nil, errf("attempt to call a %s value", typeName(h))
	}
	return st.callMetaHandler(th, h, []value.Value{b, c}, 1)
}

// callMetaHandler 调用一个元方法 handler(Lua 或 host),取 nWant 个返回值
// (nWant=1 取首值;0 不取)。
//
// Lua handler 走 host→Lua 重入(05 §7.3:新一层 execute,Go 栈 +1)。
func (st *State) callMetaHandler(th *thread, fn value.Value, args []value.Value, nWant int) (value.Value, *LuaError) {
	results, e := st.callLuaFromHost(th, fn, args)
	if e != nil {
		return value.Nil, e
	}
	if nWant == 0 || len(results) == 0 {
		return value.Nil, nil
	}
	return results[0], nil
}

// callLuaFromHost 从 host 上下文发起一次 Lua/host 调用(05 §7.3)。
//
// 把 fn+args 推到栈顶,enterLuaFrame(entry=true) 后新起一层 execute。
// 这是"Go 栈 +1"的 host→Lua 重入边界。非函数经 __call 转发(07)。
func (st *State) callLuaFromHost(th *thread, fn value.Value, args []value.Value) ([]value.Value, *LuaError) {
	if value.Tag(fn) != value.TagFunction {
		h := st.metaFieldOfValue(fn, "__call")
		if value.Tag(h) != value.TagFunction {
			return nil, errf("attempt to call a %s value", typeName(fn))
		}
		args = append([]value.Value{fn}, args...)
		fn = h
	}
	cl := value.GCRefOf(fn)
	funcIdx := th.top
	need := funcIdx + 1 + len(args)
	th.ensureStack(need)
	th.stack[funcIdx] = fn
	for i, a := range args {
		th.stack[funcIdx+1+i] = a
	}
	th.top = need
	if isHost := st.isHostClosure(cl); isHost {
		if e := st.callHost(th, funcIdx, len(args), -1); e != nil {
			return nil, e
		}
		n := th.top - funcIdx
		out := make([]value.Value, n)
		copy(out, th.stack[funcIdx:th.top])
		th.top = funcIdx
		return out, nil
	}
	savedDepth := len(th.cis)
	if e := st.enterLuaFrame(th, funcIdx, len(args), -1, true); e != nil {
		return nil, e
	}
	if e := st.execute(th); e != nil {
		// 失败:回滚 CallInfo 到进入前(05 §9.3 protected 边界清理职责)
		th.cis = th.cis[:savedDepth]
		th.top = funcIdx
		return nil, e
	}
	// execute 返回后,返回值在 funcIdx 起(doReturn dst=funcIdx),top 已设
	n := th.top - funcIdx
	if n < 0 {
		n = 0
	}
	out := make([]value.Value, n)
	copy(out, th.stack[funcIdx:funcIdx+n])
	th.top = funcIdx
	return out, nil
}

func (st *State) isHostClosure(cl arena.GCRef) bool {
	return object.IsHostClosure(st.arena, cl)
}

// ProtectedCall 是 pcall 的实现核心(05 §9.3):在受保护边界内调用 fn。
//
// 错误被捕获并返回(*LuaError 非 nil);CallInfo 回滚已由 callLuaFromHost 处理。
func (st *State) ProtectedCall(fn value.Value, args []value.Value) ([]value.Value, *LuaError) {
	th := st.runningThread
	if th == nil {
		return nil, errf("pcall: no running thread")
	}
	return st.callLuaFromHost(th, fn, args)
}

// ProtectedCallDirect 与 ProtectedCall 同构(供 stdlib 内部回调 Lua 函数,
// 如 gsub 的 function repl、table.sort 的比较器)。
func (st *State) ProtectedCallDirect(fn value.Value, args []value.Value) ([]value.Value, *LuaError) {
	return st.ProtectedCall(fn, args)
}

// MetaOf 暴露 metaOf(stdlib getmetatable 用)。
func (st *State) MetaOf(t arena.GCRef) arena.GCRef { return st.metaOf(t) }

// MetaFieldOf 暴露任意 Value 的元方法查找(stdlib __tostring 等用)。
func (st *State) MetaFieldOf(v value.Value, name string) value.Value {
	return st.metaFieldOfValue(v, name)
}

// RawGet / RawSet 暴露 raw 表访问(stdlib rawget/rawset 用)。
func (st *State) RawGet(t arena.GCRef, key value.Value) (value.Value, *LuaError) {
	return st.tableGet(t, key)
}

func (st *State) RawSet(t arena.GCRef, key, val value.Value) *LuaError {
	return st.tableSet(t, key, val)
}

// RawNext 暴露迭代(stdlib next/pairs 用)。
func (st *State) RawNext(t arena.GCRef, key value.Value) (value.Value, value.Value, bool, *LuaError) {
	return st.rawNext(t, key)
}

// RawBorder 暴露 #t(stdlib table.* 用)。
func (st *State) RawBorder(t arena.GCRef) uint32 { return st.rawBorder(t) }
