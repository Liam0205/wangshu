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
func (st *State) annotateError(e *LuaError, ci *callInfo, th *thread) *LuaError {
	if e == nil || e == errYieldSentinel || e.annotated {
		return e
	}
	e.annotated = true
	// Walk up e.Level-1 frames, counting the host boundary PUC counts.
	//
	// PUC's luaB_error calls luaL_where(L, level), which names the frame LEVEL
	// steps up; level 1 (the default) is the function that called error. When the
	// walk reaches a C frame luaL_where produces an EMPTY prefix, which is why
	// error("m", 2) inside a pcall'd function reports a bare "m" -- level 2 there
	// is pcall itself.
	//
	// wangshu does not push host frames onto cis, but it does mark the frame where
	// a host call re-entered Lua (callInfo.fresh). Stepping past that marker is the
	// equivalent of reaching PUC's C frame, so the prefix is dropped there. That is
	// what makes this work without a constant offset -- an offset would only be
	// right when the intervening frame happens to be C (#197).
	if e.Level > 1 && th != nil {
		// A host boundary counts as ONE level and the walk continues past it; it
		// is not terminal. PUC's stack for pcall(f) is [f, pcall(C), caller], so
		// level 2 lands ON the C frame (empty prefix) while level 3 reaches the
		// caller and does get a prefix. Treating the boundary as terminal made
		// level 3 and above wrongly bare.
		steps := e.Level - 1
		idx := th.ciDepth - 1
		onBoundary := false
		for steps > 0 && idx >= 0 {
			cur := th.ciAt(idx)
			// Each host frame below this one counts as its own level, exactly as
			// PUC counts each C frame. One boundary can stand for several stacked
			// host frames -- pcall(pcall, f) is two -- which is why the COUNT is
			// carried per frame rather than just a boolean marker.
			for h := int(cur.hostFrames); h > 0 && steps > 0; h-- {
				steps--
				if steps == 0 {
					onBoundary = true
				}
			}
			if steps == 0 {
				break
			}
			// A tail-called frame replaced its caller, so the caller is gone from
			// the stack. PUC still counts that vanished level but has no position
			// for it (luaL_where yields nothing for a tail-call frame), so the walk
			// consumes one more step here and reports no prefix if it lands there.
			if cur.Tailcall() {
				steps--
				if steps == 0 {
					onBoundary = true
					break
				}
			}
			idx--
			steps--
		}
		if onBoundary || idx < 0 {
			if !e.HasValue {
				e.Value = value.MakeGC(value.TagString, st.gc.Intern([]byte(e.Msg)))
				e.HasValue = true
			}
			return e
		}
		up := th.ciAt(idx)
		ci = &up
	}
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
