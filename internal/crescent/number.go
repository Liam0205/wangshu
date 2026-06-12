// parseLuaNumber — string→number 的唯一入口(07 §5.2:算术 coercion、数值
// for、tonumber 三处共用一套,行为一致由单一实现保证)。
//
// 规则(对齐 5.1 lvm.c luaV_tonumber → luaO_str2d):前后空白修剪;十进制
// 浮点(strtod 语义)或 0x 十六进制整数;其余失败。
package crescent

import (
	"strconv"
	"strings"
)

// parseLuaNumberBytes 解析字节串;成功返回 (f, true)。
func parseLuaNumberBytes(b []byte) (float64, bool) {
	return ParseLuaNumber(string(b))
}

// ParseLuaNumber 解析 Lua 数字串(暴露给 stdlib 的 tonumber 共用)。
func ParseLuaNumber(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	// 0x / 0X 十六进制(5.1 整数十六进制;大小写不限)
	neg := false
	t := s
	if len(t) > 0 && (t[0] == '+' || t[0] == '-') {
		neg = t[0] == '-'
		t = t[1:]
	}
	if len(t) > 2 && t[0] == '0' && (t[1] == 'x' || t[1] == 'X') {
		u, err := strconv.ParseUint(t[2:], 16, 64)
		if err != nil {
			return 0, false
		}
		f := float64(u)
		if neg {
			f = -f
		}
		return f, true
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}
