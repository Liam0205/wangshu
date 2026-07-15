// Error position annotation + traceback (09).
package crescent

import (
	"fmt"
	"strings"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// annotateError prepends a "chunkname:line:" prefix to a runtime error (09 error-catalog wording).
//
// Only applied to "interpreter-intrinsic errors" (type errors, etc.) and error(msg, level≥1);
// not applied when error(v) carries a non-string value or level=0 (5.1 semantics). The same
// error is annotated only once.
func (st *State) annotateError(e *LuaError, ci *callInfo) *LuaError {
	if e == nil || e == errYieldSentinel || e.annotated {
		return e
	}
	e.annotated = true
	proto := st.protoOf(ci)
	src := bytecode.ChunkID(proto.Source)
	line := int32(0)
	pc := int(ci.pc) - 1
	if pc >= 0 && pc < len(proto.LineInfo) {
		line = proto.LineInfo[pc]
	}
	prefix := fmt.Sprintf("%s:%d: ", src, line)
	e.Msg = prefix + e.Msg
	// Interpreter-intrinsic errors (HasValue=false): error value = the prefixed Msg;
	// a string value carried by error(v) (Level≠0) gets the prefix too; a non-string
	// error value (including nil/false/0 — HasValue distinguishes "carries nil" from
	// "not set") is left unchanged (5.1).
	if !e.HasValue {
		e.Value = value.MakeGC(value.TagString, st.gc.Intern([]byte(e.Msg)))
		e.HasValue = true
	} else if value.Tag(e.Value) == value.TagString && e.Level != 0 {
		raw := object.StringBytes(st.arena, value.GCRefOf(e.Value))
		e.Value = value.MakeGC(value.TagString, st.gc.Intern(append([]byte(prefix), raw...)))
	}
	return e
}

// buildTraceback builds the call-stack traceback (09: chunkname:line + [C] frames).
func (st *State) buildTraceback(th *thread) string {
	var sb strings.Builder
	sb.WriteString("stack traceback:")
	for i := th.ciDepth - 1; i >= 0; i-- {
		ci := th.ciAt(i)
		sb.WriteString("\n\t")
		// Everything pushed onto cis is a Lua frame (host frames are not pushed onto cis); protoID is always valid.
		proto := st.protoOf(&ci)
		line := int32(0)
		pc := int(ci.pc) - 1
		if pc >= 0 && pc < len(proto.LineInfo) {
			line = proto.LineInfo[pc]
		}
		what := "function"
		if i == 0 {
			what = "main chunk"
		}
		if ci.Tailcall() {
			sb.WriteString("(...tail calls...)\n\t")
		}
		fmt.Fprintf(&sb, "%s:%d: in %s", bytecode.ChunkID(proto.Source), line, what)
	}
	return sb.String()
}

// Traceback is exposed to stdlib (the P1 form of debug.traceback).
func (st *State) Traceback() string {
	if st.runningThread == nil {
		return "stack traceback:"
	}
	return st.buildTraceback(st.runningThread)
}
