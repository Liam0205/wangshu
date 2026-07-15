package gc

import "github.com/Liam0205/wangshu/internal/value"

// Push registers a temporarily held arena reference as a root (06 §6.2).
// Returns a handle (== current stack depth) used for pairing checks.
//
// Host code pattern:
//
//	ref := arena.NewString(...)
//	h := gc.Push(value.MakeGC(value.TagString, ref))
//	defer gc.Pop(h)
//	... use ref ...
func (c *Collector) Push(v value.Value) int {
	c.shadow = append(c.shadow, v)
	return len(c.shadow)
}

// Pop unwinds to depth handle-1. It only does a range check, not an
// "equals the most recent Push return value" check: out-of-order pops
// (popping an outer handle first) silently truncate deeper registrations —
// LIFO pairing is guaranteed by the caller's defer discipline (06 §6.3);
// here we only guard against going out of range.
func (c *Collector) Pop(handle int) {
	if handle <= 0 || handle > len(c.shadow) {
		// Defensive: a link that skipped its pairing gets truncated, and the
		// handles of subsequent Push/Pop calls will all be off.
		// This is the fallback error entry point for 06 §6.3 host discipline
		// violations; P1 panics, and once M11 host fns are wired in this becomes vm.raise.
		panic("gc: shadow stack pop out of range")
	}
	c.shadow = c.shadow[:handle-1]
}

// ShadowDepth returns the current shadow stack depth (diagnostic).
func (c *Collector) ShadowDepth() int { return len(c.shadow) }

// WriteBarrier is the placeholder write-barrier interface (06 §9.4).
//
// P1: no-op (STW GC, no incremental marking → three-color invariant trivially holds).
// P3+ (incremental GC): if isBlack(parent) && isWhite(child) && incrementalMarking { ... }.
//
// Wangshu does not replicate Go's runtime.gcWriteBarrier (roadmap §6 non-goal):
// arena-internal references are GCRef integers, and the Go compiler will **not**
// insert barriers for them, so we pay none of Go's barrier tax.
func (c *Collector) WriteBarrier(parent, child value.Value) {
	_ = parent
	_ = child
	// P1 no-op.
}
