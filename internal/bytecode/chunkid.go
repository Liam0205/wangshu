// ChunkID — isomorphic implementation of the official luaO_chunkid (the
// display form of chunkname in error messages).
package bytecode

// Buffer sizes luaO_chunkid is called with in 5.1.5, including the NUL.
const (
	// IDSize is LUA_IDSIZE: runtime error positions, tracebacks and debug.getinfo's short_src.
	IDSize = 60
	// MaxSrc is llex.c's MAXSRC: luaX_lexerror, which every lexer, parser and code-generator
	// error goes through, formats the chunk name with this larger buffer.
	MaxSrc = 80
)

// ChunkID converts a raw chunkname into the display form used as a runtime error message prefix
// (LUA_IDSIZE). See ChunkIDN.
func ChunkID(source string) string { return ChunkIDN(source, IDSize) }

// ChunkIDN is luaO_chunkid(out, source, bufflen):
//   - "=name" → name verbatim, cut to bufflen-1 bytes;
//   - "@file" → the file name; if longer than bufflen - sizeof(" '...' "), its tail with a "..."
//     prefix;
//   - otherwise → [string "first line"], with "..." when the source has more than its first line
//     or that line is longer than bufflen - sizeof(" [string \"...\"] ").
//
// An empty source still shows as "?" (lua5.1 gives [string ""]; tracked as #284).
func ChunkIDN(source string, bufflen int) string {
	if source == "" {
		return "?"
	}
	switch source[0] {
	case '=':
		s := source[1:]
		if len(s) > bufflen-1 {
			s = s[:bufflen-1]
		}
		return s
	case '@':
		s := source[1:]
		keep := bufflen - len(" '...' ") - 1 // sizeof counts the NUL
		if len(s) > keep {
			return "..." + s[len(s)-keep:]
		}
		return s
	default:
		keep := bufflen - len(` [string "..."] `) - 1 // sizeof counts the NUL
		n := len(source)
		for i := 0; i < len(source); i++ {
			if source[i] == '\n' || source[i] == '\r' {
				n = i
				break
			}
		}
		if n > keep {
			n = keep
		}
		if n < len(source) {
			return `[string "` + source[:n] + `..."]`
		}
		return `[string "` + source + `"]`
	}
}
