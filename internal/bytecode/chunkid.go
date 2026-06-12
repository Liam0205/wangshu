// ChunkID — 官方 luaO_chunkid 的同构实现(错误消息里的 chunkname 显示形态)。
package bytecode

// chunkIDLen 对齐官方 LUA_IDSIZE-1(buf 60 含 NUL)。
const chunkIDLen = 59

// ChunkID 把原始 chunkname 转为错误消息前缀显示形态:
//   - "=name" → name 原样(去 '=',截断到上限);
//   - "@file" → 文件名(过长保尾部,前缀 "...");
//   - 其它   → [string "首行内容"],超长或含换行截断加 "..."。
func ChunkID(source string) string {
	if source == "" {
		return "?"
	}
	switch source[0] {
	case '=':
		s := source[1:]
		if len(s) > chunkIDLen {
			s = s[:chunkIDLen]
		}
		return s
	case '@':
		s := source[1:]
		if len(s) > chunkIDLen-3 {
			return "..." + s[len(s)-(chunkIDLen-3):]
		}
		return s
	default:
		// [string "..."] 形态:取首行,官方预算 = IDSIZE - sizeof(" [string \"...\"] ")
		const budget = chunkIDLen - len(` [string "..."] `)
		line := source
		truncated := false
		for i := 0; i < len(line); i++ {
			if line[i] == '\n' || line[i] == '\r' {
				line = line[:i]
				truncated = true
				break
			}
		}
		if len(line) > budget {
			line = line[:budget]
			truncated = true
		}
		if truncated {
			return `[string "` + line + `..."]`
		}
		return `[string "` + line + `"]`
	}
}
