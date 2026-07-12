// parseLuaNumber — string→number 的唯一入口(07 §5.2:算术 coercion、数值
// for、tonumber 三处共用一套,行为一致由单一实现保证)。
//
// 规则(对齐 5.1 lobject.c luaO_str2d):C99 strtod 语义 + 'x' 停点十六进制
// 整数回退 + 尾随空白容忍。strtod 的接受面比十进制 ParseFloat 宽:
//   - 十六进制浮点:`0x.8` = 0.5、`0X.0` = 0、`0x1p4` = 16(p 指数可选);
//   - inf / infinity / nan 字样(大小写不限);
//   - 溢出返回 ±inf(luaO_str2d 不查 errno:`1e999` = inf)。
//
// cgo oracle 差分 fuzz 撞出旧实现(十进制 ParseFloat + 0x 整数专线)对
// `tonumber("0X.0")` 返回 nil 的分歧。
package crescent

import (
	"errors"
	"strconv"
	"strings"
)

// parseLuaNumberBytes 解析字节串;成功返回 (f, true)。
func parseLuaNumberBytes(b []byte) (float64, bool) {
	return ParseLuaNumber(string(b))
}

// ParseLuaNumber 解析 Lua 数字串(暴露给 stdlib 的 tonumber 共用)。
func ParseLuaNumber(s string) (float64, bool) {
	i := 0
	for i < len(s) && isLuaSpace(s[i]) {
		i++
	}
	rest := s[i:]
	f, n, ok := strtodPrefix(rest)
	if !ok {
		return 0, false
	}
	// luaO_str2d:strtod 停在 'x'/'X' ⟹ 从串头按 strtoul(s, 16) 重解,
	// 之后仍走同一 endptr 契约(跳尾随空白,余任何字符 ⟹ 失败)。
	// strtoul 语义:可选符号 + 可选 "0x" 前缀 + 最长十六进制位串;
	// 溢出饱和 ULONG_MAX。C99 下本回退几乎恒失败(整串合法的 hex 已被
	// strtod 的 hex float 吃掉;"1X0"/"0x" 这类停点形状 strtoul 也
	// 消费不完整串)——oracle diff fuzz 撞出旧实现(盲目取 [2:] 当
	// hex 位串)把 tonumber("1X0") 解成 0 的分歧。
	if n < len(rest) && (rest[n] == 'x' || rest[n] == 'X') {
		u, n2, ok2 := strtoulHexPrefix(rest)
		if !ok2 {
			return 0, false
		}
		for n2 < len(rest) && isLuaSpace(rest[n2]) {
			n2++
		}
		if n2 != len(rest) {
			return 0, false
		}
		return u, true
	}
	for n < len(rest) && isLuaSpace(rest[n]) {
		n++
	}
	if n != len(rest) {
		return 0, false
	}
	return f, true
}

// strtoulHexPrefix 模拟 C strtoul(s, &end, 16) 的前缀解析:可选符号 +
// 可选 "0x"/"0X" 前缀 + 最长十六进制位串。返回 (值, 消耗字节数, 成功);
// 溢出饱和 ULONG_MAX(strtoul 语义,不查 errno 对位 luaO_str2d);
// 负号按 C 无符号回绕后转 double。
func strtoulHexPrefix(s string) (float64, int, bool) {
	i := 0
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	digitStart := i
	if i+1 < len(s) && s[i] == '0' && (s[i+1] == 'x' || s[i+1] == 'X') && i+2 < len(s) && isHexDigitByte(s[i+2]) {
		i += 2
		digitStart = i
	}
	u := uint64(0)
	overflow := false
	for i < len(s) && isHexDigitByte(s[i]) {
		d := hexVal(s[i])
		if u > (^uint64(0)-uint64(d))/16 {
			overflow = true
		}
		u = u*16 + uint64(d)
		i++
	}
	if i == digitStart {
		return 0, 0, false
	}
	if overflow {
		u = ^uint64(0)
	}
	f := float64(u)
	if neg {
		f = float64(-u) // C unsigned negation wraps, then cast_num
	}
	return f, i, true
}

// hexVal 返回十六进制位的数值(caller 已验 isHexDigitByte)。
func hexVal(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}

// isLuaSpace 对齐 C isspace(默认 locale)。
func isLuaSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\v' || c == '\f' || c == '\r'
}

// strtodPrefix 解析 s 的最长 C99 strtod 前缀。返回 (值, 消耗字节数, 成功)。
func strtodPrefix(s string) (float64, int, bool) {
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	// inf / infinity / nan(大小写不限;infinity 优先最长匹配)
	for _, w := range [...]string{"infinity", "inf", "nan"} {
		if len(s[i:]) >= len(w) && strings.EqualFold(s[i:i+len(w)], w) {
			end := i + len(w)
			f, _ := strconv.ParseFloat(canonWord(s[:i], w), 64)
			return f, end, true
		}
	}
	// 十六进制(0x 前缀)
	if i+1 < len(s) && s[i] == '0' && (s[i+1] == 'x' || s[i+1] == 'X') {
		j := i + 2
		digits := 0
		for j < len(s) && isHexDigitByte(s[j]) {
			j++
			digits++
		}
		if j < len(s) && s[j] == '.' {
			j++
			for j < len(s) && isHexDigitByte(s[j]) {
				j++
				digits++
			}
		}
		if digits == 0 {
			// "0x" 无有效位数:strtod 只消耗 "0",停点是 'x'。
			return 0, i + 1, true
		}
		end := j
		hasP := false
		if j < len(s) && (s[j] == 'p' || s[j] == 'P') {
			k := j + 1
			if k < len(s) && (s[k] == '+' || s[k] == '-') {
				k++
			}
			expDigits := 0
			for k < len(s) && s[k] >= '0' && s[k] <= '9' {
				k++
				expDigits++
			}
			if expDigits > 0 {
				end = k
				hasP = true
			}
		}
		text := s[:end]
		if !hasP {
			text += "p0" // Go 十六进制浮点强制要求 p 指数;strtod 不要求
		}
		return evalFloat(text, end)
	}
	// 十进制
	j := i
	digits := 0
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
		digits++
	}
	if j < len(s) && s[j] == '.' {
		j++
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
			digits++
		}
	}
	if digits == 0 {
		return 0, 0, false
	}
	end := j
	if j < len(s) && (s[j] == 'e' || s[j] == 'E') {
		k := j + 1
		if k < len(s) && (s[k] == '+' || s[k] == '-') {
			k++
		}
		expDigits := 0
		for k < len(s) && s[k] >= '0' && s[k] <= '9' {
			k++
			expDigits++
		}
		if expDigits > 0 {
			end = k
		}
	}
	return evalFloat(s[:end], end)
}

// canonWord 把 inf/nan 词形规约成 Go ParseFloat 认识的形态(带原符号)。
func canonWord(sign, w string) string {
	if strings.EqualFold(w, "nan") {
		return "nan" // Go 不接受带符号 nan;符号位无语义
	}
	return sign + "inf"
}

// evalFloat 求值一段形状已合法的数字文本(consumed = 原串消耗字节数,
// 可能小于 len(text):hex 无 p 时内部补了 "p0")。溢出按 strtod 语义
// 返回 ±inf 接受(ErrRange 时 ParseFloat 已给出 ±Inf/0)。
func evalFloat(text string, consumed int) (float64, int, bool) {
	f, err := strconv.ParseFloat(text, 64)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) {
			return f, consumed, true
		}
		return 0, 0, false
	}
	return f, consumed, true
}

// isHexDigitByte 判断十六进制位。
func isHexDigitByte(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}
