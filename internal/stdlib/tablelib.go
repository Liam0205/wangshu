// table / os / io 子库 + base 库补全(unpack/xpcall)(10 裁剪表必做列)。
package stdlib

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Liam0205/wangshu/internal/crescent"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// ----- table 子库 -----

var tableFns = []entry{
	{"insert", tableFnInsert},
	{"remove", tableFnRemove},
	{"concat", tableFnConcat},
	{"sort", tableFnSort},
	{"getn", tableFnGetn},
	{"maxn", tableFnMaxn},
	{"setn", tableFnSetn},
	{"foreach", tableFnForeach},   // LUA_COMPAT 5.0 遗留(官方 5.1.5 默认带)
	{"foreachi", tableFnForeachi}, // 同上
}

// tableFnForeach:table.foreach(t, f) —— 对每个键值对调 f(k, v),f 返回
// 非 nil 即中止并返回该值(官方 ltablib foreach)。
func tableFnForeach(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	tv, e := tblArg(args, 0, "foreach")
	if e != nil {
		return nil, e
	}
	if len(args) < 2 || value.Tag(args[1]) != value.TagFunction {
		return nil, crescent.NewError("bad argument #2 to 'foreach' (function expected)")
	}
	t := value.GCRefOf(tv)
	key := value.Nil
	for {
		k, v, ok, err := st.RawNext(t, key)
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		rs, e := st.ProtectedCallDirect(args[1], []value.Value{k, v})
		if e != nil {
			return nil, e
		}
		if len(rs) > 0 && rs[0] != value.Nil {
			return []value.Value{rs[0]}, nil
		}
		key = k
	}
	return nil, nil
}

// tableFnForeachi:table.foreachi(t, f) —— 对 1..#t 调 f(i, t[i]),返回值
// 语义同 foreach。
func tableFnForeachi(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	tv, e := tblArg(args, 0, "foreachi")
	if e != nil {
		return nil, e
	}
	if len(args) < 2 || value.Tag(args[1]) != value.TagFunction {
		return nil, crescent.NewError("bad argument #2 to 'foreachi' (function expected)")
	}
	t := value.GCRefOf(tv)
	n := int(st.RawBorder(t))
	for i := 1; i <= n; i++ {
		v, _ := st.RawGet(t, value.NumberValue(float64(i)))
		rs, e := st.ProtectedCallDirect(args[1], []value.Value{value.NumberValue(float64(i)), v})
		if e != nil {
			return nil, e
		}
		if len(rs) > 0 && rs[0] != value.Nil {
			return []value.Value{rs[0]}, nil
		}
	}
	return nil, nil
}

// tableFnSetn:table.setn —— 5.1.5 实测直接报 "'setn' is obsolete"
// (10 §11 △ 列写"空操作",但 oracle 行为优先:对齐报错措辞保差分)。
func tableFnSetn(_ *crescent.State, _ []value.Value) ([]value.Value, *crescent.LuaError) {
	return nil, crescent.NewError("'setn' is obsolete") // 带位置前缀(executeFrom 注解)
}

func tblArg(args []value.Value, n int, fname string) (value.Value, *crescent.LuaError) {
	if n >= len(args) || value.Tag(args[n]) != value.TagTable {
		return value.Nil, crescent.NewError(fmt.Sprintf("bad argument #%d to '%s' (table expected)", n+1, fname))
	}
	return args[n], nil
}

// tableFnInsert:table.insert(t, [pos,] v)。
func tableFnInsert(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	tv, e := tblArg(args, 0, "insert")
	if e != nil {
		return nil, e
	}
	t := value.GCRefOf(tv)
	n := int(st.RawBorder(t))
	switch len(args) {
	case 2:
		// append
		if e := st.RawSet(t, value.NumberValue(float64(n+1)), args[1]); e != nil {
			return nil, e
		}
	case 3:
		posF, ok := toNumberStr(st, args[1])
		if !ok {
			return nil, crescent.NewError("bad argument #2 to 'insert' (number expected)")
		}
		pos := int(posF)
		if pos < 1 || pos > n+1 {
			return nil, crescent.NewError("bad argument #2 to 'insert' (position out of bounds)")
		}
		// 右移 [pos, n] → [pos+1, n+1]
		for i := n; i >= pos; i-- {
			v, _ := st.RawGet(t, value.NumberValue(float64(i)))
			if e := st.RawSet(t, value.NumberValue(float64(i+1)), v); e != nil {
				return nil, e
			}
		}
		if e := st.RawSet(t, value.NumberValue(float64(pos)), args[2]); e != nil {
			return nil, e
		}
	default:
		return nil, crescent.NewError("wrong number of arguments to 'insert'")
	}
	return nil, nil
}

// tableFnRemove:table.remove(t [, pos]) → 被移除的值。
func tableFnRemove(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	tv, e := tblArg(args, 0, "remove")
	if e != nil {
		return nil, e
	}
	t := value.GCRefOf(tv)
	n := int(st.RawBorder(t))
	pos := n
	if len(args) >= 2 {
		posF, ok := toNumberStr(st, args[1])
		if !ok {
			return nil, crescent.NewError("bad argument #2 to 'remove' (number expected)")
		}
		pos = int(posF)
	}
	// 越界 pos(含空表):返回 0 个值且不动表(官方 tremove
	// `!(1 <= pos && pos <= e) return 0`)。旧实现无此检查,位移循环
	// 不执行但 t[n]=nil 仍执行——静默删掉了末元素(数据损坏)。
	if pos < 1 || pos > n {
		return nil, nil
	}
	removed, _ := st.RawGet(t, value.NumberValue(float64(pos)))
	// 左移 [pos+1, n] → [pos, n-1]
	for i := pos; i < n; i++ {
		v, _ := st.RawGet(t, value.NumberValue(float64(i+1)))
		if e := st.RawSet(t, value.NumberValue(float64(i)), v); e != nil {
			return nil, e
		}
	}
	if e := st.RawSet(t, value.NumberValue(float64(n)), value.Nil); e != nil {
		return nil, e
	}
	return []value.Value{removed}, nil
}

// tableFnConcat:table.concat(t [, sep [, i [, j]]])。
func tableFnConcat(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	tv, e := tblArg(args, 0, "concat")
	if e != nil {
		return nil, e
	}
	t := value.GCRefOf(tv)
	sep := ""
	if len(args) >= 2 && args[1] != value.Nil {
		sb, e := strArg(st, args, 1, "concat")
		if e != nil {
			return nil, e
		}
		sep = string(sb)
	}
	iF, _ := numArg(st, args, 2, 1)
	jF, _ := numArg(st, args, 3, float64(st.RawBorder(t)))
	var parts []string
	for k := int(iF); k <= int(jF); k++ {
		v, _ := st.RawGet(t, value.NumberValue(float64(k)))
		if value.IsNumber(v) {
			parts = append(parts, crescent.FormatLuaNumber(value.AsNumber(v)))
		} else if value.Tag(v) == value.TagString {
			parts = append(parts, string(object.StringBytes(st.Arena(), value.GCRefOf(v))))
		} else {
			return nil, crescent.NewError(fmt.Sprintf("invalid value (at index %d) in table for 'concat'", k))
		}
	}
	return []value.Value{intern(st, strings.Join(parts, sep))}, nil
}

// tableFnSort:table.sort(t [, comp])。comp 是 Lua 函数(经 ProtectedCallDirect 回调)。
func tableFnSort(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	tv, e := tblArg(args, 0, "sort")
	if e != nil {
		return nil, e
	}
	t := value.GCRefOf(tv)
	n := int(st.RawBorder(t))
	vals := make([]value.Value, n)
	for i := 0; i < n; i++ {
		vals[i], _ = st.RawGet(t, value.NumberValue(float64(i+1)))
	}
	var sortErr *crescent.LuaError
	less := func(a, b value.Value) bool {
		if sortErr != nil {
			return false
		}
		if len(args) >= 2 && value.Tag(args[1]) == value.TagFunction {
			rs, e := st.ProtectedCallDirect(args[1], []value.Value{a, b})
			if e != nil {
				sortErr = e
				return false
			}
			return len(rs) > 0 && value.Truthy(rs[0])
		}
		// 默认比较走完整 `<` 语义(数字/字符串快路径 + __lt 元方法,
		// 官方 sort_comp 经 lua_lessthan;带 __lt 的对象表可直接 sort)
		r, e := st.LessThan(a, b)
		if e != nil {
			sortErr = e
			return false
		}
		return r
	}
	sort.SliceStable(vals, func(i, j int) bool { return less(vals[i], vals[j]) })
	if sortErr != nil {
		return nil, sortErr
	}
	for i := 0; i < n; i++ {
		if e := st.RawSet(t, value.NumberValue(float64(i+1)), vals[i]); e != nil {
			return nil, e
		}
	}
	return nil, nil
}

// tableFnGetn:table.getn(t)(5.1 遗留,= #t)。
func tableFnGetn(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	tv, e := tblArg(args, 0, "getn")
	if e != nil {
		return nil, e
	}
	return []value.Value{value.NumberValue(float64(st.RawBorder(value.GCRefOf(tv))))}, nil
}

// tableFnMaxn:table.maxn(t)= 最大正数键(遍历全表,5.1)。
func tableFnMaxn(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	tv, e := tblArg(args, 0, "maxn")
	if e != nil {
		return nil, e
	}
	t := value.GCRefOf(tv)
	maxn := 0.0
	key := value.Nil
	for {
		k, _, ok, err := st.RawNext(t, key)
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		if value.IsNumber(k) {
			if f := value.AsNumber(k); f > maxn {
				maxn = f
			}
		}
		key = k
	}
	return []value.Value{value.NumberValue(maxn)}, nil
}

// ----- os / io 最小集 -----

var osFns = []entry{
	{"time", osFnTime},
	{"clock", osFnClock},
	{"date", osFnDate},
	{"getenv", osFnGetenv},
	{"difftime", osFnDifftime},
}

// osFnDifftime:os.difftime(t2, t1) = t2 - t1(POSIX 秒,5.1)。
func osFnDifftime(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) < 1 {
		return nil, crescent.NewError("bad argument #1 to 'difftime'")
	}
	t2, ok := toNumberStr(st, args[0])
	if !ok {
		return nil, crescent.NewError("bad argument #1 to 'difftime' (number expected)")
	}
	t1 := 0.0
	if len(args) >= 2 && args[1] != value.Nil {
		var ok2 bool
		t1, ok2 = toNumberStr(st, args[1])
		if !ok2 {
			return nil, crescent.NewError("bad argument #2 to 'difftime' (number expected)")
		}
	}
	return []value.Value{value.NumberValue(t2 - t1)}, nil
}

func osFnTime(_ *crescent.State, _ []value.Value) ([]value.Value, *crescent.LuaError) {
	return []value.Value{value.NumberValue(float64(time.Now().Unix()))}, nil
}

var processStart = time.Now()

func osFnClock(_ *crescent.State, _ []value.Value) ([]value.Value, *crescent.LuaError) {
	return []value.Value{value.NumberValue(time.Since(processStart).Seconds())}, nil
}

func osFnDate(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	format := "%c"
	if len(args) >= 1 && args[0] != value.Nil {
		fb, e := strArg(st, args, 0, "date")
		if e != nil {
			return nil, e
		}
		format = string(fb)
	}
	now := time.Now()
	// 极简 strftime 子集(%Y %m %d %H %M %S %c)
	r := strings.NewReplacer(
		"%Y", fmt.Sprintf("%04d", now.Year()),
		"%m", fmt.Sprintf("%02d", int(now.Month())),
		"%d", fmt.Sprintf("%02d", now.Day()),
		"%H", fmt.Sprintf("%02d", now.Hour()),
		"%M", fmt.Sprintf("%02d", now.Minute()),
		"%S", fmt.Sprintf("%02d", now.Second()),
		"%c", now.Format("Mon Jan  2 15:04:05 2006"),
	)
	return []value.Value{intern(st, r.Replace(format))}, nil
}

func osFnGetenv(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	nb, e := strArg(st, args, 0, "getenv")
	if e != nil {
		return nil, e
	}
	v, ok := os.LookupEnv(string(nb))
	if !ok {
		return []value.Value{value.Nil}, nil
	}
	return []value.Value{intern(st, v)}, nil
}

var ioFns = []entry{
	{"write", ioFnWrite},
}

func ioFnWrite(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	for i := range args {
		b, e := strArg(st, args, i, "write")
		if e != nil {
			return nil, e
		}
		_, _ = os.Stdout.Write(b)
	}
	return nil, nil
}

// ----- math 补全 -----

var mathExtraFns = []entry{
	{"fmod", mathFnFmod},
	{"mod", mathFnFmod}, // LUA_COMPAT_MOD:5.0 别名(官方 5.1.5 默认带)
	{"modf", mathFnModf},
	{"atan2", mathFn2("atan2", atan2)},
	{"sinh", mathFn1("sinh", sinh)},
	{"cosh", mathFn1("cosh", cosh)},
	{"tanh", mathFn1("tanh", tanh)},
	{"frexp", mathFnFrexp},
	{"ldexp", mathFn2("ldexp", func(m, e float64) float64 { return ldexp(m, int(e)) })},
	{"pow", mathFn2("pow", func(a, b float64) float64 { return pow(a, b) })},
	{"random", mathFnRandom},
	{"randomseed", mathFnRandomSeed},
	{"atan", mathFn1("atan", atan)},
	{"asin", mathFn1("asin", asin)},
	{"acos", mathFn1("acos", acos)},
	{"deg", mathFn1("deg", deg)},
	{"rad", mathFn1("rad", rad)},
	{"log10", mathFn1("log10", log10)},
}

func mathFn2(name string, f func(a, b float64) float64) crescent.HostFn {
	return func(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
		if len(args) < 2 {
			return nil, crescent.NewError(fmt.Sprintf("bad argument #%d to '%s' (number expected, got no value)", len(args)+1, name))
		}
		a, ok1 := toNumberStr(st, args[0])
		if !ok1 {
			return nil, crescent.NewError(fmt.Sprintf("bad argument #1 to '%s' (number expected, got %s)", name, crescent.TypeNameOf(args[0])))
		}
		b, ok2 := toNumberStr(st, args[1])
		if !ok2 {
			return nil, crescent.NewError(fmt.Sprintf("bad argument #2 to '%s' (number expected, got %s)", name, crescent.TypeNameOf(args[1])))
		}
		return []value.Value{value.NumberValue(f(a, b))}, nil
	}
}

func mathFnFmod(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	return mathFn2("fmod", fmod)(st, args)
}

func mathFnFrexp(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) < 1 {
		return nil, crescent.NewError("bad argument #1 to 'frexp'")
	}
	x, ok := toNumberStr(st, args[0])
	if !ok {
		return nil, crescent.NewError("bad argument #1 to 'frexp' (number expected)")
	}
	m, e := frexp(x)
	return []value.Value{value.NumberValue(m), value.NumberValue(float64(e))}, nil
}

func mathFnModf(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) < 1 {
		return nil, crescent.NewError("bad argument #1 to 'modf'")
	}
	x, ok := toNumberStr(st, args[0])
	if !ok {
		return nil, crescent.NewError("bad argument #1 to 'modf' (number expected)")
	}
	ip, fp := modf(x)
	return []value.Value{value.NumberValue(ip), value.NumberValue(fp)}, nil
}

func mathFnRandom(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	switch len(args) {
	case 0:
		return []value.Value{value.NumberValue(rngFloat())}, nil
	case 1:
		m, ok := toNumberStr(st, args[0])
		if !ok || m < 1 {
			return nil, crescent.NewError("bad argument #1 to 'random' (interval is empty)")
		}
		return []value.Value{value.NumberValue(float64(rngInt(1, int64(m))))}, nil
	default:
		lo, ok1 := toNumberStr(st, args[0])
		hi, ok2 := toNumberStr(st, args[1])
		if !ok1 || !ok2 || lo > hi {
			return nil, crescent.NewError("bad argument #2 to 'random' (interval is empty)")
		}
		return []value.Value{value.NumberValue(float64(rngInt(int64(lo), int64(hi))))}, nil
	}
}

func mathFnRandomSeed(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) >= 1 {
		if f, ok := toNumberStr(st, args[0]); ok {
			rngSeed(int64(f))
		}
	}
	return nil, nil
}

// ----- base 补全:unpack / xpcall -----

// baseFnUnpackImpl:unpack(t [, i [, j]])。
func baseFnUnpackImpl(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	tv, e := tblArg(args, 0, "unpack")
	if e != nil {
		return nil, e
	}
	t := value.GCRefOf(tv)
	iF, _ := numArg(st, args, 1, 1)
	jF, _ := numArg(st, args, 2, float64(st.RawBorder(t)))
	i, j := int(iF), int(jF)
	if i > j {
		return nil, nil // 空区间
	}
	// 范围上限对齐官方 LUAI_MAXCSTACK(luaconf.h,经 lua_checkstack 拒绝):
	// unpack({},1,100000) 官方报错;同时防 2^30 级区间分配巨型切片拖死进程。
	n := j - i + 1
	if n <= 0 || n > 8000 {
		return nil, crescent.NewError("too many results to unpack")
	}
	out := make([]value.Value, 0, n)
	for k := i; k <= j; k++ {
		v, _ := st.RawGet(t, value.NumberValue(float64(k)))
		out = append(out, v)
	}
	return out, nil
}

// baseFnXpcall:xpcall(f, handler) → (true, results...) | (false, handler(err))。
//
// 09 语义:handler 在栈展开前调用——P1 实现为"捕获后立刻调用 handler"
// (栈已由 protected 边界回滚;P1 不支持 handler 内 inspect 出错栈帧,
// 这是已记录的简化,见 implementation-progress)。
func baseFnXpcall(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) < 2 {
		return nil, crescent.NewError("bad argument #2 to 'xpcall' (value expected)")
	}
	fn, handler := args[0], args[1]
	results, e := st.ProtectedCall(fn, nil)
	if e == nil {
		out := make([]value.Value, 0, len(results)+1)
		out = append(out, value.True)
		out = append(out, results...)
		return out, nil
	}
	errVal := e.Value
	if !e.HasValue {
		errVal = intern(st, e.Msg)
	}
	hres, he := st.ProtectedCall(handler, []value.Value{errVal})
	if he != nil {
		return []value.Value{value.False, intern(st, "error in error handling")}, nil
	}
	out := make([]value.Value, 0, len(hres)+1)
	out = append(out, value.False)
	out = append(out, hres...)
	return out, nil
}
