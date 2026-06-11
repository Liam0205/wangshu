// string 库的 pattern 相关函数 + format/byte/char(10 §7-§8)。
package stdlib

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Liam0205/wangshu/internal/crescent"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// strArg 取第 n 个参数为 string 字节(数字自动转字符串,Lua 5.1 行为)。
func strArg(st *crescent.State, args []value.Value, n int, fname string) ([]byte, *crescent.LuaError) {
	if n >= len(args) {
		return nil, crescent.NewError(fmt.Sprintf("bad argument #%d to '%s' (string expected, got no value)", n+1, fname))
	}
	v := args[n]
	if value.Tag(v) == value.TagString {
		return object.StringBytes(st.Arena(), value.GCRefOf(v)), nil
	}
	if value.IsNumber(v) {
		return []byte(crescent.FormatLuaNumber(value.AsNumber(v))), nil
	}
	return nil, crescent.NewError(fmt.Sprintf("bad argument #%d to '%s' (string expected, got %s)",
		n+1, fname, crescent.TypeNameOf(v)))
}

// numArg 取第 n 个参数为 number(可缺省)。
func numArg(st *crescent.State, args []value.Value, n int, def float64) (float64, bool) {
	if n >= len(args) || args[n] == value.Nil {
		return def, true
	}
	return toNumberStr(st, args[n])
}

// strInitPos 把 Lua 的 init 参数(1-based,可负)规约为 0-based 下标。
func strInitPos(init float64, slen int) int {
	i := int(init)
	if i < 0 {
		i = slen + i + 1
	}
	if i < 1 {
		i = 1
	}
	return i - 1
}

// capsToValues 把捕获物化为 Lua 值;无显式捕获时返回整个匹配串。
func capsToValues(st *crescent.State, src []byte, s, e int, caps []capResult) []value.Value {
	if len(caps) == 0 {
		return []value.Value{intern(st, string(src[s:e]))}
	}
	out := make([]value.Value, len(caps))
	for i, c := range caps {
		if c.pos {
			out[i] = value.NumberValue(float64(c.start + 1))
		} else {
			out[i] = intern(st, string(src[c.start:c.start+c.len]))
		}
	}
	return out
}

// stringFnFind:string.find(s, pat [, init [, plain]])。
func stringFnFind(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	s, e := strArg(st, args, 0, "find")
	if e != nil {
		return nil, e
	}
	pat, e := strArg(st, args, 1, "find")
	if e != nil {
		return nil, e
	}
	initF, ok := numArg(st, args, 2, 1)
	if !ok {
		return nil, crescent.NewError("bad argument #3 to 'find' (number expected)")
	}
	init := strInitPos(initF, len(s))
	if init > len(s) {
		return []value.Value{value.Nil}, nil
	}
	plain := len(args) >= 4 && value.Truthy(args[3])
	if plain {
		idx := strings.Index(string(s[init:]), string(pat))
		if idx < 0 {
			return []value.Value{value.Nil}, nil
		}
		start := init + idx
		return []value.Value{
			value.NumberValue(float64(start + 1)),
			value.NumberValue(float64(start + len(pat))),
		}, nil
	}
	start, end, caps, found, err := patternFind(s, pat, init)
	if err != nil {
		return nil, crescent.NewError(err.Error())
	}
	if !found {
		return []value.Value{value.Nil}, nil
	}
	out := []value.Value{
		value.NumberValue(float64(start + 1)),
		value.NumberValue(float64(end)),
	}
	if len(caps) > 0 {
		out = append(out, capsToValues(st, s, start, end, caps)...)
	}
	return out, nil
}

// stringFnMatch:string.match(s, pat [, init])。
func stringFnMatch(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	s, e := strArg(st, args, 0, "match")
	if e != nil {
		return nil, e
	}
	pat, e := strArg(st, args, 1, "match")
	if e != nil {
		return nil, e
	}
	initF, ok := numArg(st, args, 2, 1)
	if !ok {
		return nil, crescent.NewError("bad argument #3 to 'match' (number expected)")
	}
	init := strInitPos(initF, len(s))
	if init > len(s) {
		return []value.Value{value.Nil}, nil
	}
	start, end, caps, found, err := patternFind(s, pat, init)
	if err != nil {
		return nil, crescent.NewError(err.Error())
	}
	if !found {
		return []value.Value{value.Nil}, nil
	}
	return capsToValues(st, s, start, end, caps), nil
}

// stringFnGmatch:string.gmatch(s, pat) → 迭代器闭包。
//
// 迭代器是 host closure,经 State 注册;状态(下次起点)收在 Go 闭包变量。
func stringFnGmatch(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	s, e := strArg(st, args, 0, "gmatch")
	if e != nil {
		return nil, e
	}
	pat, e := strArg(st, args, 1, "gmatch")
	if e != nil {
		return nil, e
	}
	src := append([]byte(nil), s...)
	p := append([]byte(nil), pat...)
	pos := 0
	iter := func(ist *crescent.State, _ []value.Value) ([]value.Value, *crescent.LuaError) {
		if pos > len(src) {
			return []value.Value{value.Nil}, nil
		}
		start, end, caps, found, err := patternFind(src, p, pos)
		if err != nil {
			return nil, crescent.NewError(err.Error())
		}
		if !found {
			pos = len(src) + 1
			return []value.Value{value.Nil}, nil
		}
		if end == pos && end == start {
			pos = end + 1 // 空匹配:推进一格防死循环
		} else {
			pos = end
		}
		return capsToValues(ist, src, start, end, caps), nil
	}
	id := st.RegisterHostFn(iter)
	cl := st.MakeHostClosure(id)
	return []value.Value{value.MakeGC(value.TagFunction, cl)}, nil
}

// stringFnGsub:string.gsub(s, pat, repl [, n])。repl 支持 string/function/table。
func stringFnGsub(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	s, e := strArg(st, args, 0, "gsub")
	if e != nil {
		return nil, e
	}
	pat, e := strArg(st, args, 1, "gsub")
	if e != nil {
		return nil, e
	}
	if len(args) < 3 {
		return nil, crescent.NewError("bad argument #3 to 'gsub' (string/function/table expected)")
	}
	repl := args[2]
	maxN := -1
	if len(args) >= 4 && args[3] != value.Nil {
		f, ok := toNumberStr(st, args[3])
		if !ok {
			return nil, crescent.NewError("bad argument #4 to 'gsub' (number expected)")
		}
		maxN = int(f)
	}
	var out []byte
	pos := 0
	count := 0
	for (maxN < 0 || count < maxN) && pos <= len(s) {
		start, end, caps, found, err := patternFind(s, pat, pos)
		if err != nil {
			return nil, crescent.NewError(err.Error())
		}
		if !found {
			break
		}
		out = append(out, s[pos:start]...)
		rep, le := st2gsubRepl(st, s, start, end, caps, repl)
		if le != nil {
			return nil, le
		}
		out = append(out, rep...)
		count++
		if end == start {
			if start < len(s) {
				out = append(out, s[start])
			}
			pos = start + 1
		} else {
			pos = end
		}
	}
	if pos < len(s) {
		out = append(out, s[pos:]...)
	}
	return []value.Value{intern(st, string(out)), value.NumberValue(float64(count))}, nil
}

// st2gsubRepl 计算一次替换文本。
func st2gsubRepl(st *crescent.State, src []byte, s, e int, caps []capResult, repl value.Value) ([]byte, *crescent.LuaError) {
	whole := src[s:e]
	capVal := func(i int) value.Value {
		vals := capsToValues(st, src, s, e, caps)
		if i < len(vals) {
			return vals[i]
		}
		return value.Nil
	}
	switch {
	case value.Tag(repl) == value.TagString || value.IsNumber(repl):
		var rb []byte
		if value.IsNumber(repl) {
			rb = []byte(crescent.FormatLuaNumber(value.AsNumber(repl)))
		} else {
			rb = object.StringBytes(st.Arena(), value.GCRefOf(repl))
		}
		var out []byte
		for i := 0; i < len(rb); i++ {
			if rb[i] == '%' && i+1 < len(rb) {
				i++
				c := rb[i]
				if c == '%' {
					out = append(out, '%')
				} else if c >= '0' && c <= '9' {
					if c == '0' {
						out = append(out, whole...)
					} else {
						v := capVal(int(c - '1'))
						b, _ := valueToBytesForGsub(st, v)
						out = append(out, b...)
					}
				} else {
					return nil, crescent.NewError("invalid use of '%' in replacement string")
				}
			} else {
				out = append(out, rb[i])
			}
		}
		return out, nil
	case value.Tag(repl) == value.TagFunction:
		vals := capsToValues(st, src, s, e, caps)
		results, le := st.ProtectedCallDirect(repl, vals)
		if le != nil {
			return nil, le
		}
		if len(results) == 0 || results[0] == value.Nil || results[0] == value.False {
			return whole, nil
		}
		b, ok := valueToBytesForGsub(st, results[0])
		if !ok {
			return nil, crescent.NewError("invalid replacement value (a " + crescent.TypeNameOf(results[0]) + ")")
		}
		return b, nil
	case value.Tag(repl) == value.TagTable:
		key := capVal(0)
		v, le := st.RawGet(value.GCRefOf(repl), key)
		if le != nil {
			return nil, le
		}
		if v == value.Nil || v == value.False {
			return whole, nil
		}
		b, ok := valueToBytesForGsub(st, v)
		if !ok {
			return nil, crescent.NewError("invalid replacement value (a " + crescent.TypeNameOf(v) + ")")
		}
		return b, nil
	}
	return nil, crescent.NewError("bad argument #3 to 'gsub' (string/function/table expected)")
}

func valueToBytesForGsub(st *crescent.State, v value.Value) ([]byte, bool) {
	if value.IsNumber(v) {
		return []byte(crescent.FormatLuaNumber(value.AsNumber(v))), true
	}
	if value.Tag(v) == value.TagString {
		return object.StringBytes(st.Arena(), value.GCRefOf(v)), true
	}
	return nil, false
}

// stringFnFormat:string.format(fmt, ...) — %d %i %u %f %g %e %s %q %x %X %o %c %%。
func stringFnFormat(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	f, e := strArg(st, args, 0, "format")
	if e != nil {
		return nil, e
	}
	var out []byte
	argn := 1
	i := 0
	for i < len(f) {
		if f[i] != '%' {
			out = append(out, f[i])
			i++
			continue
		}
		i++
		if i < len(f) && f[i] == '%' {
			out = append(out, '%')
			i++
			continue
		}
		// 取 flags/width/precision
		spec := []byte{'%'}
		for i < len(f) && strings.ContainsRune("-+ #0", rune(f[i])) {
			spec = append(spec, f[i])
			i++
		}
		for i < len(f) && isdigit(f[i]) {
			spec = append(spec, f[i])
			i++
		}
		if i < len(f) && f[i] == '.' {
			spec = append(spec, f[i])
			i++
			for i < len(f) && isdigit(f[i]) {
				spec = append(spec, f[i])
				i++
			}
		}
		if i >= len(f) {
			return nil, crescent.NewError("invalid format string to 'format'")
		}
		verb := f[i]
		i++
		if argn >= len(args) && verb != '%' {
			return nil, crescent.NewError(fmt.Sprintf("bad argument #%d to 'format' (no value)", argn+1))
		}
		switch verb {
		case 'd', 'i':
			n, ok := toNumberStr(st, args[argn])
			if !ok {
				return nil, crescent.NewError(fmt.Sprintf("bad argument #%d to 'format' (number expected)", argn+1))
			}
			out = append(out, []byte(fmt.Sprintf(string(append(spec, 'd')), int64(n)))...)
			argn++
		case 'u':
			n, _ := toNumberStr(st, args[argn])
			out = append(out, []byte(fmt.Sprintf(string(append(spec, 'd')), int64(n)))...)
			argn++
		case 'x', 'X', 'o':
			n, _ := toNumberStr(st, args[argn])
			out = append(out, []byte(fmt.Sprintf(string(append(spec, verb)), int64(n)))...)
			argn++
		case 'c':
			n, _ := toNumberStr(st, args[argn])
			out = append(out, byte(int64(n)))
			argn++
		case 'f', 'F', 'e', 'E', 'g', 'G':
			n, ok := toNumberStr(st, args[argn])
			if !ok {
				return nil, crescent.NewError(fmt.Sprintf("bad argument #%d to 'format' (number expected)", argn+1))
			}
			out = append(out, []byte(fmt.Sprintf(string(append(spec, verb)), n))...)
			argn++
		case 's':
			sv := valueToString(st, args[argn])
			out = append(out, []byte(fmt.Sprintf(string(append(spec, 's')), sv))...)
			argn++
		case 'q':
			sb, e2 := strArg(st, args, argn, "format")
			if e2 != nil {
				return nil, e2
			}
			out = append(out, quoteLuaString(sb)...)
			argn++
		default:
			return nil, crescent.NewError(fmt.Sprintf("invalid option '%%%c' to 'format'", verb))
		}
	}
	return []value.Value{intern(st, string(out))}, nil
}

// quoteLuaString 实现 %q(对齐 5.1 的安全引号转义)。
func quoteLuaString(s []byte) []byte {
	out := []byte{'"'}
	for _, c := range s {
		switch c {
		case '"':
			out = append(out, '\\', '"')
		case '\\':
			out = append(out, '\\', '\\')
		case '\n':
			out = append(out, '\\', 'n')
		case '\r':
			out = append(out, '\\', 'r')
		case 0:
			out = append(out, '\\', '0')
		default:
			out = append(out, c)
		}
	}
	return append(out, '"')
}

// stringFnByte:string.byte(s [, i [, j]])。
func stringFnByte(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	s, e := strArg(st, args, 0, "byte")
	if e != nil {
		return nil, e
	}
	iF, _ := numArg(st, args, 1, 1)
	jF, _ := numArg(st, args, 2, iF)
	i := normIdx(int(iF), len(s))
	j := normIdx(int(jF), len(s))
	if i < 1 {
		i = 1
	}
	if j > len(s) {
		j = len(s)
	}
	var out []value.Value
	for k := i; k <= j; k++ {
		out = append(out, value.NumberValue(float64(s[k-1])))
	}
	return out, nil
}

// stringFnChar:string.char(...)。
func stringFnChar(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	out := make([]byte, len(args))
	for i, a := range args {
		f, ok := toNumberStr(st, a)
		if !ok {
			return nil, crescent.NewError(fmt.Sprintf("bad argument #%d to 'char' (number expected)", i+1))
		}
		out[i] = byte(int64(f))
	}
	return []value.Value{intern(st, string(out))}, nil
}

// 保留 strconv 引用(strInitPos 等未来扩展)。
var _ = strconv.Itoa
