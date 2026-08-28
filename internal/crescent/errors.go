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
		// Prefixing copies and re-interns the whole message, so a loop raising a 1 MiB error
		// copied a megabyte per raise: 20000 of them ran 16 seconds untripped, while the same
		// loop with error(s, 0) -- no prefix, no copy -- took 2.9s. Charged on the shared byte
		// meter like every other bulk copy.
		_ = st.chargeBulkWork(len(prefix) + len(raw))
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
		// Host frames below this one are real stack entries PUC shows as "[C]: in ?" -- a
		// traceback taken from inside table.foreach or a sort comparator omitted the C frame
		// entirely. This is the third reader of this stack to need the hostFrames count; the
		// other two are error()'s level walk and resolveLevel.
		for h := int(ci.hostFrames); h > 0; h-- {
			sb.WriteString("\n\t[C]: in ?")
		}
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
	// Resolve through the SAME frame model getinfo uses, so a level counts the C and tail
	// pseudo-frames rather than just Lua frames. Subtracting from ciDepth directly landed on
	// the wrong frame whenever a host frame sat in between -- a traceback taken at a level
	// from inside table.foreach or a sort comparator skipped past the C frame PUC shows.
	idx, kind, ok := st.resolveLevel(level)
	if !ok {
		return "stack traceback:"
	}
	// Landing ON a pseudo-frame means the visible stack starts at the Lua frame below it,
	// which is the one resolveLevel returned.
	keep := idx + 1
	if kind == frameLua {
		keep = idx + 1
	}
	if keep <= 0 || keep > th.ciDepth {
		return "stack traceback:"
	}
	saved := th.ciDepth
	th.ciDepth = keep
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
	idx, kind, ok := st.resolveLevel(level)
	if !ok || kind != frameLua {
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
	idx, kind, ok := st.resolveLevel(level)
	if !ok || kind != frameLua {
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

// resolveLevel maps a getinfo/traceback LEVEL onto a cis index, counting the host (C) frames
// that sit between Lua frames.
//
// Indexing cis directly by ciDepth-level SKIPS those, which reorders everything above the first
// one: with a Lua callback invoked from table.foreach, PUC's chain is
// [inner, callback, foreach(C), main] and a direct index reported [inner, callback, main, C] --
// so what/func/source/currentline all named the wrong function from level 3 up.
//
// callInfo.hostFrames already records how many host frames sit immediately below each frame (it
// was added for error()'s level walk), so the mapping is a walk rather than new bookkeeping.
// A tail call also inserts a pseudo-frame: PUC keeps the vanished caller visible as what="tail",
// and callInfo.tailDepth records how many frames a chain collapsed (added for error()'s walk).
//
// Returns (index, kind, ok) where kind is frameLua, frameHost (a C frame, no cis entry,
// what="C") or frameTail (the pseudo-frame of a tail-called chain, what="tail").
func (st *State) resolveLevel(level int) (int, frameKind, bool) {
	th := st.runningThread
	if th == nil || level < 1 {
		return 0, frameLua, false
	}
	remaining := level
	for idx := th.ciDepth - 1; idx >= 0; idx-- {
		remaining--
		if remaining == 0 {
			return idx, frameLua, true
		}
		ci := th.ciAt(idx)
		// Each frame a tail-call chain replaced is one visible level with no position.
		for d := int(ci.tailDepth); d > 0; d-- {
			remaining--
			if remaining == 0 {
				return idx, frameTail, true
			}
		}
		// Host frames below this Lua frame each consume one level.
		for h := int(ci.hostFrames); h > 0; h-- {
			remaining--
			if remaining == 0 {
				return idx, frameHost, true
			}
		}
	}
	// One past the bottom is the host that entered the interpreter -- but only on the main
	// thread. A coroutine has its own stack whose bottom was reached by resume from ANOTHER
	// thread, and PUC's getinfo stops there rather than reporting a C frame: inside
	// coroutine.wrap, level 2 is already nil.
	if remaining == 1 && len(st.threadChain) == 0 {
		return -1, frameHost, true
	}
	return 0, frameLua, false
}

// frameKind distinguishes the three things a debug level can name.
type frameKind uint8

const (
	frameLua  frameKind = iota // a real cis frame
	frameHost                  // a C frame, which wangshu does not push onto cis
	frameTail                  // the pseudo-frame PUC keeps for a tail call's vanished caller
)

// FrameIsHostBoundary reports whether LEVEL is exactly one step past the outermost Lua frame,
// i.e. the host call that entered the interpreter. debug.getinfo reports what="C" there.
func (st *State) FrameIsHostBoundary(level int) bool {
	_, kind, ok := st.resolveLevel(level)
	return ok && kind == frameHost
}

// FunctionIsHost reports whether a function VALUE is a host (C-equivalent) closure rather than a Lua one.
//
// The frame-based FrameIsHostBoundary above answers the same question for an active level; this answers it
// for a function passed by value, which is what debug.getinfo(f) needs. PUC distinguishes these with
// `isLua(ci)` on a frame and `cl->c.isC` on a value, and it has both because the two forms are asked
// independently. Returns false for a non-function.
func (st *State) FunctionIsHost(v value.Value) bool {
	if value.Tag(v) != value.TagFunction {
		return false
	}
	return object.IsHostClosure(st.arena, value.GCRefOf(v))
}

// FunctionActiveLines returns the set of lines a Lua function has code on, as PUC's `activelines` table does:
// keys are line numbers with value true. ok is false for a host function or a non-function.
//
// This is derived, not fabricated: LineInfo already maps every instruction to its line, and PUC builds the same
// table the same way (currentline is exactly a lookup into it). The official db.lua asserts that a function's
// first and last lines are both present, which is what made the gap visible -- it had been grouped with
// nups/namewhat as "hook-dependent and therefore omitted", but unlike those it needs nothing beyond the proto.
func (st *State) FunctionActiveLines(v value.Value) (map[int32]bool, bool) {
	if value.Tag(v) != value.TagFunction {
		return nil, false
	}
	ref := value.GCRefOf(v)
	if object.IsHostClosure(st.arena, ref) {
		return nil, false
	}
	pid := object.ClosureProtoID(st.arena, ref)
	if int(pid) >= len(st.protos) {
		return nil, false
	}
	proto := st.protos[pid]
	lines := make(map[int32]bool, len(proto.LineInfo))
	for _, ln := range proto.LineInfo {
		if ln > 0 {
			lines[ln] = true
		}
	}
	return lines, true
}

// FunctionInfo returns a Lua function's raw source, its display form (as ChunkID renders it, which is what
// short_src holds) and its linedefined. ok is false for a host function or a non-function.
//
// Returned as one call rather than three accessors so the three values cannot be fetched from different
// protos, and so short_src goes through bytecode.ChunkID here -- the same helper the frame-based path uses,
// rather than a second copy of the "=name"/"@file" stripping rules in the stdlib package.
func (st *State) FunctionInfo(v value.Value) (source, shortSrc string, lineDefined, lastLineDefined int32, ok bool) {
	if value.Tag(v) != value.TagFunction {
		return "", "", 0, 0, false
	}
	ref := value.GCRefOf(v)
	if object.IsHostClosure(st.arena, ref) {
		return "", "", 0, 0, false
	}
	pid := object.ClosureProtoID(st.arena, ref)
	if int(pid) >= len(st.protos) {
		return "", "", 0, 0, false
	}
	proto := st.protos[pid]
	// LineEnd is the `end` line, already tracked for every Proto: PUC's lastlinedefined. A main chunk carries
	// 0 for both, which is what PUC reports and what distinguishes what="main".
	last := proto.LineEnd
	if proto.LineDefined == 0 {
		last = 0
	}
	return proto.Source, bytecode.ChunkID(proto.Source), proto.LineDefined, last, true
}

// FrameIsTail reports whether LEVEL names a tail call's vanished caller, which getinfo
// describes as what="tail".
func (st *State) FrameIsTail(level int) bool {
	_, kind, ok := st.resolveLevel(level)
	return ok && kind == frameTail
}

// FrameLineDefined returns the line the frame's function was defined on (0 for a main chunk).
func (st *State) FrameLineDefined(level int) (int32, bool) {
	th := st.runningThread
	if th == nil || level < 1 {
		return 0, false
	}
	idx, kind, ok := st.resolveLevel(level)
	if !ok || kind != frameLua {
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
	idx, kind, ok := st.resolveLevel(level)
	if !ok || kind != frameLua {
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
	idx, kind, ok := st.resolveLevel(level)
	if !ok || kind != frameLua {
		return value.Nil, false
	}
	ci := th.ciAt(idx)
	if ci.cl == 0 {
		return value.Nil, false
	}
	return value.MakeGC(value.TagFunction, ci.cl), true
}
