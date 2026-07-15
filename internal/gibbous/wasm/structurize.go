//go:build wangshu_p3

package wasm

// Structured-emit layer (02-translation §3.3/§3.5, PW4 relooper): reconstruct
// the multi-BB reducible CFG into nested Wasm block/loop + br depth.
//
// Algorithm (LLVM WebAssemblyCFGStackify / Binaryen Stackify route, correct for
// reducible CFGs):
//   Phase A: layoutOrder — a loop-contiguous emit order (CFGSort). The raw RPO
//            may split a loop body across BBs outside the loop, so a loop cannot
//            be wrapped directly; during the DFS post-order, visit "successors
//            leaving the current loop first, successors staying inside later" so
//            the loop body is contiguous in emit order.
//   Phase B: computeScopes — compute the scope interval [begin,end] (emit-order
//            positions) of every loop header / forward-merge BB, guaranteeing
//            scopes are pairwise either disjoint or nested.
//   Phase C: emit — scope-stack driven: emit BB by BB, at boundaries close (end)
//            first then open (loop/block); a BB's terminating edges go through
//            br depth (the target scope's relative position in the stack).
//
// **Safety valve**: any shape this layer cannot structure correctly (irreducible /
// br target not on the stack / illegal scope nesting) returns an error → Compile
// turns it into CompileError → P2 fallback interpreter (byte-equal holds
// trivially). The partial implementation is therefore always safe — it just
// promotes fewer Protos.

import (
	"fmt"
	"sort"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// scopeKind is the kind of scope.
type scopeKind int

const (
	scLoop  scopeKind = iota // loop: br to it = jump back to the start (continue/back-edge)
	scBlock                  // block: br to it = jump past its end (forward jump/merge)
	scIf                     // if: takes one br-depth level (no target, not a br target)
)

// scope is a scope interval (closed emit-order position range [begin,end]).
type scope struct {
	kind   scopeKind
	target int // loop: header BB id; block: merge BB id; if: meaningless (-1)
	begin  int // emit-order start position (loop/block is emitted before this position)
	end    int // emit-order end position (end is emitted after this position)
}

// structPlan is the output of structural analysis (layoutOrder + scopes),
// consumed by the emit layer.
type structPlan struct {
	order  []int   // emit order (BB id)
	pos    []int   // BB id → emit-order position
	begins [][]int // position → indices of scopes starting there (outer first, see computeScopes)
	scopes []scope // all loop/block scopes (if is not here, pushed at runtime)
}

// layoutOrder produces a loop-contiguous emit order (Phase A).
func (r *relooper) layoutOrder() ([]int, []int) {
	n := len(r.c.blocks)
	visited := make([]bool, n)
	var post []int
	var dfs func(u int)
	dfs = func(u int) {
		visited[u] = true
		lu := r.loopOf[u] // innermost loop header containing u (-1 = not in a loop)
		succs := append([]int(nil), r.c.blocks[u].succs...)
		// Sort keys: ① successors staying in lu go later (visited later → earlier
		// in post-order → later in RPO = loop body contiguous right after the
		// header) ② id ascending (determinism).
		sort.SliceStable(succs, func(i, j int) bool {
			si, sj := r.staysIn(succs[i], lu), r.staysIn(succs[j], lu)
			if si != sj {
				return !si // the one leaving (false) is visited first
			}
			return succs[i] < succs[j]
		})
		for _, v := range succs {
			if v >= 0 && v < n && !visited[v] {
				dfs(v)
			}
		}
		post = append(post, u)
	}
	dfs(r.c.entry)

	order := make([]int, 0, len(post))
	for i := len(post) - 1; i >= 0; i-- {
		order = append(order, post[i])
	}
	pos := make([]int, n)
	for i := range pos {
		pos[i] = -1
	}
	for i, bb := range order {
		pos[bb] = i
	}
	return order, pos
}

// staysIn reports whether s belongs to the body of loop h (h=-1 always false).
func (r *relooper) staysIn(s, h int) bool {
	if h < 0 {
		return false
	}
	li := r.loops[h]
	if li == nil {
		return false
	}
	for _, b := range li.body {
		if b == s {
			return true
		}
	}
	return false
}

// isReducible is an O(E) reducibility check: on the reachable subgraph every
// "back-going edge" (rpoIndex[v] ≤ rpoIndex[u]) must be a back-edge (v dominates
// u); otherwise it is a multi-entry loop (irreducible) → reject.
func (r *relooper) isReducible() bool {
	reach := r.c.reachableBlocks()
	for _, bb := range r.c.blocks {
		if !reach[bb.id] {
			continue
		}
		for _, s := range bb.succs {
			if !reach[s] {
				continue
			}
			if r.rpoIndex[s] <= r.rpoIndex[bb.id] {
				// back-going edge (incl. self-loop): must be a back-edge (target dominates source)
				if !r.dominates(s, bb.id) {
					return false
				}
			}
		}
	}
	return true
}

// computeScopes computes loop / block scopes and their open order (Phase B).
//
// Returns an error when a shape this layer cannot structure safely appears
// (should fall back).
func (r *relooper) computeScopes(order, pos []int) ([]scope, [][]int, error) {
	n := len(r.c.blocks)
	var scopes []scope

	// --- loop scopes: each header covers [pos[h], max body pos] ---
	for h, li := range r.loops {
		if pos[h] < 0 {
			continue // unreachable (in theory never, loops only contains reachable)
		}
		end := pos[h]
		for _, b := range li.body {
			if pos[b] > end {
				end = pos[b]
			}
		}
		scopes = append(scopes, scope{kind: scLoop, target: h, begin: pos[h], end: end})
	}

	// --- block scopes: a merge BB with a "non-fallthrough forward in-edge"
	// needs a block ---
	// Forward in-edge u→d: pos[u] < pos[d] and not a natural fallthrough
	// (pos[u]+1==pos[d] and d is u's sequential successor).
	preds := r.predsOf()
	for _, d := range order {
		var fwd []int
		for _, p := range preds[d] {
			if pos[p] < 0 || pos[p] >= pos[d] {
				continue // not forward (back-edge / same position)
			}
			// Natural fallthrough: p immediately precedes d, and p's terminator
			// is not "jump away" (whether fallthrough is possible is decided by
			// the emit layer); here we conservatively treat "pos[p]+1==pos[d]" as
			// possibly fallthrough, but only skip the block when d is p's sole
			// forward destination. A multi-predecessor merge still needs a block.
			fwd = append(fwd, p)
		}
		if len(fwd) == 0 {
			continue
		}
		// Need a block: covers [minPredPos, pos[d]-1], br to it lands on d.
		begin := pos[d]
		for _, p := range fwd {
			if pos[p] < begin {
				begin = pos[p]
			}
		}
		if begin >= pos[d] {
			continue // no forward crossing (fallthrough only), no block needed
		}
		scopes = append(scopes, scope{kind: scBlock, target: d, begin: begin, end: pos[d] - 1})
	}

	// --- Normalize: widen block begins until all scopes are pairwise
	// disjoint or nested (issue #91) ---
	if err := normalizeScopes(scopes); err != nil {
		return nil, nil, err
	}

	// --- verify scopes are pairwise disjoint or nested ---
	for i := range scopes {
		for j := i + 1; j < len(scopes); j++ {
			if overlapImproper(scopes[i], scopes[j]) {
				return nil, nil, fmt.Errorf("p4 relooper: improper scope overlap %v vs %v", scopes[i], scopes[j])
			}
		}
	}

	// --- begins index: position → scope index, outer opened first (larger end
	// first; equal end then smaller begin first; then block-outside-loop and
	// farther target as a determinism tiebreaker) ---
	begins := make([][]int, n)
	for idx, s := range scopes {
		begins[s.begin] = append(begins[s.begin], idx)
	}
	for p := range begins {
		ids := begins[p]
		sort.SliceStable(ids, func(a, b int) bool {
			sa, sb := scopes[ids[a]], scopes[ids[b]]
			if sa.end != sb.end {
				return sa.end > sb.end // larger end is the outer scope, opened first
			}
			// Same interval: the merge point with larger pos (merges later) is the outer scope, opened first
			return pos[sa.target] > pos[sb.target]
		})
	}
	return scopes, begins, nil
}

// overlapImproper reports whether two scopes "partially overlap" (neither
// disjoint nor nested).
func overlapImproper(a, b scope) bool {
	// disjoint
	if a.end < b.begin || b.end < a.begin {
		return false
	}
	// a contains b or b contains a
	if (a.begin <= b.begin && b.end <= a.end) || (b.begin <= a.begin && a.end <= b.end) {
		return false
	}
	return true // partial overlap
}

// normalizeScopes widens partially-overlapping scopes into containment
// (issue #91).
//
// Physical constraints: a block's end is pinned (the br landing point
// must sit right before the merge BB) but its begin can move earlier
// freely (an extra Wasm nesting level has no semantic cost — br depths
// adapt via the emit-time stack lookup); a loop's begin AND end are
// both pinned (header position + body extent). So for any partially
// overlapping pair, the only fix is for the scope with the larger end
// to move its begin back until it contains the other — and that scope
// must be a block. A loop having the larger end would mean a forward
// edge lands in the middle of a loop body (a second loop entry), which
// isReducible already rejects; the error arm here is defensive.
//
// The old repair only checked one direction at block-construction time
// (an EXISTING scope's begin falling inside the NEW scope's range),
// missing the symmetric case (an earlier scope's end falling inside a
// later scope's range) — exactly what a top-level if/else diamond's two
// merge blocks produce, so those CFAILed outright (issue #91). Run a
// fixed-point pass over all pairs after every scope is built instead,
// covering both directions.
//
// Termination: each repair strictly decreases some block's begin and
// begins are bounded below by 0, so the fixed-point iteration halts.
func normalizeScopes(scopes []scope) error {
	changed := true
	for changed {
		changed = false
		for i := range scopes {
			for j := i + 1; j < len(scopes); j++ {
				if !overlapImproper(scopes[i], scopes[j]) {
					continue
				}
				// outer = the one with the larger end (must absorb
				// the other); equal ends cannot be improper (one
				// necessarily contains the other then).
				oi := i
				if scopes[j].end > scopes[i].end {
					oi = j
				}
				inner := i + j - oi
				if scopes[oi].kind != scBlock {
					return fmt.Errorf("p4 relooper: improper scope overlap %v vs %v (outer is not a block)",
						scopes[i], scopes[j])
				}
				scopes[oi].begin = scopes[inner].begin
				changed = true
			}
		}
	}
	return nil
}

// buildStructPlan runs Phase A+B to produce the structured plan (called by translate).
func buildStructPlan(c *cfg) (*structPlan, error) {
	r := analyzeRelooper(c)
	if !r.isReducible() {
		return nil, fmt.Errorf("p4 relooper: irreducible CFG (multi-entry loop)")
	}
	order, pos := r.layoutOrder()
	scopes, begins, err := r.computeScopes(order, pos)
	if err != nil {
		return nil, err
	}
	return &structPlan{order: order, pos: pos, begins: begins, scopes: scopes}, nil
}

// emitStructured is Phase C: scope-stack driven emit (Compiler method, calls emitOpcode).
func (c *Compiler) emitStructured(em *emitter, proto *bytecode.Proto, cfg *cfg, plan *structPlan) error {
	var stack []scope
	for i, bb := range plan.order {
		// ① Close scopes ending before position i (stack top = innermost, closed first).
		for len(stack) > 0 {
			top := stack[len(stack)-1]
			if top.kind == scIf {
				// if pairs its own end when emitted by the compare/FOR path, not closed here.
				break
			}
			if top.end < i {
				em.end()
				stack = stack[:len(stack)-1]
			} else {
				break
			}
		}
		// ② Open scopes starting at position i (begins is already sorted outer-first).
		for _, sidx := range plan.begins[i] {
			s := plan.scopes[sidx]
			switch s.kind {
			case scLoop:
				em.loop()
			case scBlock:
				em.block()
			}
			stack = append(stack, s)
		}
		// ③ Emit the BB body + terminating edges.
		if err := c.emitBlockBody(em, proto, cfg, plan, bb, &stack); err != nil {
			return err
		}
	}
	// flush remaining scopes.
	for len(stack) > 0 {
		if stack[len(stack)-1].kind != scIf {
			em.end()
		}
		stack = stack[:len(stack)-1]
	}
	return nil
}

// brDepth computes the relative stack depth of the scope corresponding to dst
// (loop header or block merge point). Wasm: br 0 = innermost (stack top);
// returns (depth, true) or (0, false).
func brDepth(stack []scope, dst int, kind scopeKind) (uint32, bool) {
	for t := len(stack) - 1; t >= 0; t-- {
		if stack[t].kind == kind && stack[t].target == dst {
			return uint32(len(stack) - 1 - t), true
		}
	}
	return 0, false
}

// emitEdge emits one CFG edge src→dst: if fallthrough is possible (dst is the
// next BB in emit order with no intervening scope boundary) emit no instruction;
// otherwise br to the corresponding scope (back-edge=loop / forward=block).
//
// A back-edge (dst is some loop header on the stack) is the only loop back-edge
// choke point — all loop shapes (numeric FORLOOP / while / repeat / compare-driven
// JMP back-edge) go through here to br back to the loop header. The step-budget
// billing safepoint must hang here, before the `br`, otherwise some loop opcode's
// back-edge would miss billing (in P3 the host helper does not charge budget, the
// budget is only charged at the back-edge safepoint; the negative-displacement JMP
// back-edge of while/repeat used to miss it → fully-inlined infinite loops hang it
// permanently after promotion to P3).
func (c *Compiler) emitEdge(em *emitter, cfg *cfg, plan *structPlan, stack []scope, srcBB, dst int) error {
	// back-edge: dst is some loop header on the stack.
	if d, ok := brDepth(stack, dst, scLoop); ok {
		// back-edge step-budget + GC safepoint (hangs on the only choke point:
		// covers FORLOOP / while / repeat / compare-driven back-edges all billed,
		// the #135 family). The back-edge instruction pc = source BB's last
		// instruction pc (for error-line anchoring).
		c.emitBackEdgeSafepoint(em, cfg.blocks[srcBB].endPC-1)
		em.br(d)
		return nil
	}
	// forward merge: dst is past some block's end on the stack.
	if d, ok := brDepth(stack, dst, scBlock); ok {
		em.br(d)
		return nil
	}
	// fallthrough: dst is exactly the next BB in emit order (natural fall-through, no instruction).
	if plan.pos[srcBB]+1 == plan.pos[dst] {
		return nil
	}
	return fmt.Errorf("p4 relooper: edge %d→%d not on scope stack and not fallthrough", srcBB, dst)
}
