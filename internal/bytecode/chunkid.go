// ChunkID — isomorphic implementation of the official luaO_chunkid (the
// display form of chunkname in error messages).
package bytecode

// chunkIDLen matches the official LUA_IDSIZE-1 (buf of 60 including the NUL).
const chunkIDLen = 59

// ChunkID converts a raw chunkname into the display form used as an error
// message prefix:
//   - "=name" → name verbatim (drop '=', truncate to the limit);
//   - "@file" → filename (if too long keep the tail, prefix "...");
//   - otherwise → [string "first line"], truncated with "..." if too long or
//     containing a newline.
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
		// [string "..."] form: take the first line, official budget = IDSIZE - sizeof(" [string \"...\"] ")
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
