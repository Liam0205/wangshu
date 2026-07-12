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
		n+1, fname, st.TypeName(v)))
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
	// PUC str_find_aux fast path: plain search when explicitly
	// requested OR when the pattern contains no SPECIALS ("^$*+?.([%-").
	// Note ')' is NOT special -- find("", ")") plain-searches and
	// returns nil where match/gsub raise "invalid pattern capture"
	// (oracle diff fuzz catch).
	plain := (len(args) >= 4 && value.Truthy(args[3])) ||
		!strings.ContainsAny(string(pat), "^$*+?.([%-")
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
		if end == start {
			// 空匹配:从命中位置 +1 推进(官方 gmatch_aux `if (e==src) newstart++`;
			// 注意命中位置可能在 pos 之后——条件是 end==start 而非 end==pos,
			// 否则扫描前进后的空匹配不推进,迭代器原地死循环重复输出)。
			pos = end + 1
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
	anchored := len(pat) > 0 && pat[0] == '^'
	for (maxN < 0 || count < maxN) && pos <= len(s) {
		start, end, caps, found, err := patternFind(s, pat, pos)
		if err != nil {
			return nil, crescent.NewError(err.Error())
		}
		// 锚点模式只在 pos 处匹配(patternFind 已保证);未中或非串首中皆停
		if !found || (anchored && start != pos) {
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
		if anchored {
			break // lstrlib:锚点 gsub 至多替换一次
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
						// %n 越界(超出捕获数;无显式捕获时仅 %1 合法 = 整匹配)
						// 官方 push_onecapture 报 invalid capture index。
						idx := int(c - '1')
						nCaps := len(caps)
						if nCaps == 0 {
							nCaps = 1 // 无显式捕获:捕获 1 = 整个匹配
						}
						if idx >= nCaps {
							return nil, crescent.NewError(fmt.Sprintf("invalid capture index %%%c", c))
						}
						v := capVal(idx)
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
			return nil, crescent.NewError("invalid replacement value (a " + st.TypeName(results[0]) + ")")
		}
		return b, nil
	case value.Tag(repl) == value.TagTable:
		key := capVal(0)
		// 经 __index 链(官方 gsub 走 lua_gettable,元方法可见)
		v, le := st.IndexWithMeta(repl, key)
		if le != nil {
			return nil, le
		}
		if v == value.Nil || v == value.False {
			return whole, nil
		}
		b, ok := valueToBytesForGsub(st, v)
		if !ok {
			return nil, crescent.NewError("invalid replacement value (a " + st.TypeName(v) + ")")
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
		// 嵌入式 hardening:width/precision 拼装在 spec 里直接交给 fmt.Sprintf,
		// `%.99999999999d` 会让 Go runtime 分配巨量字节直至 OOM 崩宿主。
		// 与 string.rep 同源,1 GiB 阈值兜住上亿位变体(width/precision 是
		// 十进制字符数,1<<30 = ~10 亿,记 maxFormatNumeric = 1 << 30)。
		const maxFormatNumeric = 1 << 30
		spec := []byte{'%'}
		for i < len(f) && strings.ContainsRune("-+ #0", rune(f[i])) {
			spec = append(spec, f[i])
			i++
		}
		widthStart := i
		for i < len(f) && isdigit(f[i]) {
			spec = append(spec, f[i])
			i++
		}
		if i-widthStart > 10 || (i > widthStart && atoiSimple(f[widthStart:i]) > maxFormatNumeric) {
			return nil, crescent.NewError("invalid format width (too large)")
		}
		if i < len(f) && f[i] == '.' {
			spec = append(spec, f[i])
			i++
			precStart := i
			for i < len(f) && isdigit(f[i]) {
				spec = append(spec, f[i])
				i++
			}
			if i-precStart > 10 || (i > precStart && atoiSimple(f[precStart:i]) > maxFormatNumeric) {
				return nil, crescent.NewError("invalid format precision (too large)")
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
		case 'u', 'x', 'X', 'o':
			// PUC casts through unsigned LUA_INTFRM_T: %u/%x/%o of -1
			// print the two's-complement value, not "-1". Non-number
			// arguments raise via luaL_checknumber (oracle diff fuzz
			// caught the silently-ignored conversion failure).
			n, ok := toNumberStr(st, args[argn])
			if !ok {
				return nil, crescent.NewError(fmt.Sprintf("bad argument #%d to 'format' (number expected, got %s)", argn+1, st.TypeName(args[argn])))
			}
			v := verb
			if v == 'u' {
				v = 'd'
			}
			out = append(out, []byte(fmt.Sprintf(string(append(spec, v)), uint64(int64(n))))...)
			argn++
		case 'c':
			// PUC sprintf's the char with the full spec (width/flags
			// apply: %5c pads). Go's %c would encode bytes >= 0x80 as
			// multi-byte UTF-8, so render the single byte through %s
			// with the same spec (C %c and %s share width/flag
			// semantics for a one-char string).
			n, ok := toNumberStr(st, args[argn])
			if !ok {
				return nil, crescent.NewError(fmt.Sprintf("bad argument #%d to 'format' (number expected, got %s)", argn+1, st.TypeName(args[argn])))
			}
			out = append(out, []byte(fmt.Sprintf(string(append(spec, 's')), string([]byte{byte(int64(n))})))...)
			argn++
		case 'f', 'e', 'E', 'g', 'G':
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

// quoteLuaString 实现 %q(逐字节对齐官方 addquoted):
// `"` `\` 前置反斜杠;`\n` 输出为反斜杠+真实换行(非 \n 两字符);
// `\r` → \r;NUL → \000(三位,防后随数字粘连)。
func quoteLuaString(s []byte) []byte {
	out := []byte{'"'}
	for _, c := range s {
		switch c {
		case '"', '\\':
			out = append(out, '\\', c)
		case '\n':
			out = append(out, '\\', '\n')
		case '\r':
			out = append(out, '\\', 'r')
		case 0:
			out = append(out, '\\', '0', '0', '0')
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
	// PUC luaL_optinteger: nil defaults, but a present non-number
	// argument raises (string.byte("abc", "y") errors; "2" coerces).
	iF, ok := numArg(st, args, 1, 1)
	if !ok {
		return nil, crescent.NewError(fmt.Sprintf("bad argument #2 to 'byte' (number expected, got %s)", st.TypeName(args[1])))
	}
	jF, ok := numArg(st, args, 2, iF)
	if !ok {
		return nil, crescent.NewError(fmt.Sprintf("bad argument #3 to 'byte' (number expected, got %s)", st.TypeName(args[2])))
	}
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
		// PUC str_char: luaL_checkint truncates toward zero, then
		// luaL_argcheck(uchar(c) == c) rejects anything outside
		// [0, 255] ("invalid value"): char(-1)/char(256) error,
		// char(3.7) == char(3). Oracle diff fuzz caught the old
		// silent byte() wraparound.
		n := int64(f)
		if n < 0 || n > 255 {
			return nil, crescent.NewError(fmt.Sprintf("bad argument #%d to 'char' (invalid value)", i+1))
		}
		out[i] = byte(n)
	}
	return []value.Value{intern(st, string(out))}, nil
}

// 保留 strconv 引用(strInitPos 等未来扩展)。
var _ = strconv.Itoa
