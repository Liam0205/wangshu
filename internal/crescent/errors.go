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
		// Host frames entered since the last Lua frame push sit BELOW the raising
		// function and above the innermost Lua frame, so they are the first levels
		// the walk crosses.
		//
		// This is what a host function raising directly needs: pcall(error,"m",2)
		// has error called by pcall, so level 1 is pcall's C frame (bare, which
		// already agreed) and level 2 is the main chunk, which PUC prefixes. Those
		// boundaries are not attached to any pushed frame, so without this the walk
		// started too deep and every level came out bare.
		if e.hostRaised {
			// A host raiser occupies no Lua frame, so level 1 is its CALLER: the
			// first pending host boundary, not the innermost Lua frame. Consuming
			// these from Level-1 instead shifted every level by one --
			// pcall(error,"m",1) gained a prefix it should not have and level 2 lost
			// the one it should.
			for h := int(st.pendingHostFrames) + 1; h > 1 && steps >= 0; h-- {
				if steps == 0 {
					onBoundary = true
					break
				}
				steps--
			}
		}
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
				// A chain of N tail calls collapsed N frames into this one, and PUC
				// counts each vanished frame as a level with no position. tailDepth
				// records how many, so N chained tail calls consume N levels rather
				// than one.
				for d := int(cur.tailDepth); d > 0 && steps > 0; d-- {
					steps--
					if steps == 0 {
						onBoundary = true
					}
				}
				if steps == 0 {
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
func (st *State) Traceback() string { return st.TracebackFrom(1) }

// TracebackFrom builds a traceback that SKIPS the innermost level-1 frames, for
// debug.traceback's optional level argument. Level 1 is the default and includes everything.
func (st *State) TracebackFrom(level int) string {
	th := st.runningThread
	if th == nil {
		return "stack traceback:"
	}
	if level <= 1 {
		return st.buildTraceback(th)
	}
	skip := level - 1
	if skip >= th.ciDepth {
		return "stack traceback:"
	}
	saved := th.ciDepth
	th.ciDepth -= skip
	out := st.buildTraceback(th)
	th.ciDepth = saved
	return out
}

// FrameInfo reports the chunk name and current line of the frame LEVEL steps up from the
// caller, for debug.getinfo. ok is false when the level is past the stack.
//
// Level 1 is the function that called getinfo, matching PUC's convention. Since getinfo
// is a host function and host frames are not pushed onto cis, level 1 is the INNERMOST
// cis frame -- subtracting the level directly skipped one frame too many and returned nil
// for the common getinfo(1).
func (st *State) FrameInfo(level int) (string, int32, bool) {
	th := st.runningThread
	if th == nil || level < 1 {
		return "", 0, false
	}
	idx := th.ciDepth - level
	if idx < 0 || idx >= th.ciDepth {
		return "", 0, false
	}
	ci := th.ciAt(idx)
	proto := st.protoOf(&ci)
	line := int32(0)
	if pc := int(ci.pc) - 1; pc >= 0 && pc < len(proto.LineInfo) {
		line = proto.LineInfo[pc]
	}
	return bytecode.ChunkID(proto.Source), line, true
}

// FrameIsMain reports whether the frame LEVEL steps up is the main chunk, which
// debug.getinfo renders as what="main" rather than "Lua".
func (st *State) FrameIsMain(level int) bool {
	th := st.runningThread
	if th == nil || level < 1 {
		return false
	}
	idx := th.ciDepth - level
	if idx < 0 || idx >= th.ciDepth {
		return false
	}
	// A main chunk is the ENTRY frame of an execute layer: callInfo.fresh marks exactly that,
	// and a loadstring chunk called as a function is itself an entry, which is why lua5.1
	// reports "main" for it too.
	//
	// Three weaker tests were wrong. The frame index alone fails because a tail call replaces
	// the caller's frame, putting an ordinary function at index 0. "IsVararg with no fixed
	// parameters" fails in both directions -- it claims "main" for an ordinary function(...)
	// at the bottom of a coroutine stack and misses a loadstring chunk. And LineDefined == 0
	// is PUC's marker but not this compiler's: newFuncState records the line the chunk starts
	// on, which is 1.
	// LineDefined == 0 is the marker, set by the compiler for a chunk and never for a real
	// function definition. It is what PUC uses, and unlike the alternatives it survives both
	// a tail call putting a function at index 0 and a loadstring chunk being CALLED like a
	// function -- which lua5.1 still reports as "main", so a frame-position test cannot work.
	ci := th.ciAt(idx)
	return st.protoOf(&ci).LineDefined == 0
}

// FrameIsHostBoundary reports whether LEVEL is exactly one step past the outermost Lua frame,
// i.e. the host call that entered the interpreter. debug.getinfo reports what="C" there.
func (st *State) FrameIsHostBoundary(level int) bool {
	th := st.runningThread
	if th == nil {
		return false
	}
	return th.ciDepth-level == -1
}

// FrameLineDefined returns the line the frame's function was defined on (0 for a main chunk).
func (st *State) FrameLineDefined(level int) (int32, bool) {
	th := st.runningThread
	if th == nil || level < 1 {
		return 0, false
	}
	idx := th.ciDepth - level
	if idx < 0 || idx >= th.ciDepth {
		return 0, false
	}
	ci := th.ciAt(idx)
	return st.protoOf(&ci).LineDefined, true
}

// FrameSource returns the frame's raw source string, which carries PUC's leading marker
// ("=name" or "@file") that ChunkID strips for display. getinfo reports the raw form in
// "source" and the stripped one in "short_src", so they must not be the same value.
func (st *State) FrameSource(level int) (string, bool) {
	th := st.runningThread
	if th == nil || level < 1 {
		return "", false
	}
	idx := th.ciDepth - level
	if idx < 0 || idx >= th.ciDepth {
		return "", false
	}
	ci := th.ciAt(idx)
	return st.protoOf(&ci).Source, true
}

// FrameFunc returns the closure running in the frame LEVEL steps up, for debug.getinfo's
// "func" field. ok is false when the level is past the stack.
func (st *State) FrameFunc(level int) (value.Value, bool) {
	th := st.runningThread
	if th == nil || level < 1 {
		return value.Nil, false
	}
	idx := th.ciDepth - level
	if idx < 0 || idx >= th.ciDepth {
		return value.Nil, false
	}
	ci := th.ciAt(idx)
	if ci.cl == 0 {
		return value.Nil, false
	}
	return value.MakeGC(value.TagFunction, ci.cl), true
}
