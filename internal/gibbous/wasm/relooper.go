//go:build wangshu_p3

package wasm

// relooper: reconstructs a reducible CFG into nested Wasm structured control
// flow (02-translation §3.1.7).
//
// Algorithm (for reducible CFGs; Lua codegen output is naturally reducible):
//   1. Number BBs in reverse postorder (RPO) — guarantees a dominator appears
//      before the blocks it dominates;
//   2. Compute the dominator tree (Cooper-Harvey-Kennedy iterative fixpoint);
//   3. Detect natural loops (back edge: succ dominates pred);
//   4. Stackifier-style generation: emit BBs linearly in RPO order, using
//      nested Wasm block/loop plus br/br_if to express jumps:
//        - open a loop before the loop header; a loop back edge = br to the
//          loop label;
//        - open a block before a forward-jump target; a forward jump = br to
//          the block end label;
//        - manage nesting with a "scope stack"; the br depth is computed from
//          the target label's relative position on the stack.
//
// This implementation uses WebAssembly's classic "scope stack + on-demand
// block/loop placement" approach (cf. Binaryen Stackify / wasm-stackifier),
// which is correct for reducible CFGs.

import "sort"

// loopInfo describes a natural loop.
type loopInfo struct {
	header int   // loop header BB id
	body   []int // set of BB ids in the loop body (includes header)
}

// relooper holds the CFG analysis results.
type relooper struct {
	c        *cfg
	rpo      []int             // BB ids in reverse postorder
	rpoIndex []int             // BB id → its position in rpo
	idom     []int             // immediate dominator (BB id → BB id; entry's idom = itself)
	loops    map[int]*loopInfo // header BB id → loop
	loopOf   []int             // BB id → innermost enclosing loop header (-1 = not in any loop)
}

// analyzeRelooper runs the full analysis (RPO + dominators + loops).
func analyzeRelooper(c *cfg) *relooper {
	r := &relooper{c: c}
	r.computeRPO()
	r.computeDominators()
	r.detectLoops()
	return r
}

// computeRPO does a reverse-postorder DFS.
func (r *relooper) computeRPO() {
	n := len(r.c.blocks)
	visited := make([]bool, n)
	var post []int
	var dfs func(int)
	dfs = func(u int) {
		visited[u] = true
		// visit successors in ascending id order for determinism
		succs := append([]int(nil), r.c.blocks[u].succs...)
		sort.Ints(succs)
		for _, v := range succs {
			if !visited[v] {
				dfs(v)
			}
		}
		post = append(post, u)
	}
	dfs(r.c.entry)
	// RPO = reversed postorder
	r.rpo = make([]int, 0, len(post))
	for i := len(post) - 1; i >= 0; i-- {
		r.rpo = append(r.rpo, post[i])
	}
	r.rpoIndex = make([]int, n)
	for i := range r.rpoIndex {
		r.rpoIndex[i] = -1
	}
	for i, bb := range r.rpo {
		r.rpoIndex[bb] = i
	}
}

// computeDominators uses the Cooper-Harvey-Kennedy iterative algorithm.
func (r *relooper) computeDominators() {
	n := len(r.c.blocks)
	r.idom = make([]int, n)
	for i := range r.idom {
		r.idom[i] = -1
	}
	r.idom[r.c.entry] = r.c.entry

	// preds: predecessors of each BB
	preds := make([][]int, n)
	for _, bb := range r.c.blocks {
		for _, s := range bb.succs {
			preds[s] = append(preds[s], bb.id)
		}
	}

	changed := true
	for changed {
		changed = false
		// iterate in RPO (skip entry)
		for _, u := range r.rpo {
			if u == r.c.entry {
				continue
			}
			newIdom := -1
			for _, p := range preds[u] {
				if r.idom[p] == -1 {
					continue // predecessor not processed yet
				}
				if newIdom == -1 {
					newIdom = p
				} else {
					newIdom = r.intersect(p, newIdom)
				}
			}
			if newIdom != -1 && r.idom[u] != newIdom {
				r.idom[u] = newIdom
				changed = true
			}
		}
	}
}

// intersect finds the nearest common dominator of two BBs on the dominator
// tree (climbing up by RPO index).
func (r *relooper) intersect(a, b int) int {
	for a != b {
		for r.rpoIndex[a] > r.rpoIndex[b] {
			a = r.idom[a]
		}
		for r.rpoIndex[b] > r.rpoIndex[a] {
			b = r.idom[b]
		}
	}
	return a
}

// dominates reports whether a dominates b (climbing up the idom chain).
func (r *relooper) dominates(a, b int) bool {
	for b != -1 {
		if b == a {
			return true
		}
		if b == r.c.entry {
			return false
		}
		b = r.idom[b]
	}
	return false
}

// detectLoops finds natural loops: a back edge (u→v) where v dominates u.
// The loop body = all nodes that can reach u without passing through v, plus v.
func (r *relooper) detectLoops() {
	r.loops = make(map[int]*loopInfo)
	n := len(r.c.blocks)
	r.loopOf = make([]int, n)
	for i := range r.loopOf {
		r.loopOf[i] = -1
	}

	for _, bb := range r.c.blocks {
		for _, s := range bb.succs {
			// back edge: s dominates bb (s is the loop header, bb→s is the back jump)
			if r.dominates(s, bb.id) {
				r.addNaturalLoop(s, bb.id)
			}
		}
	}

	// mark each BB's innermost enclosing loop (the smaller the loop body, the
	// more inner it is); assign in ascending body size, so smaller loops are
	// assigned last (overwrite) → the innermost one wins
	headers := make([]int, 0, len(r.loops))
	for h := range r.loops {
		headers = append(headers, h)
	}
	sort.Slice(headers, func(i, j int) bool {
		return len(r.loops[headers[i]].body) > len(r.loops[headers[j]].body)
	})
	for _, h := range headers {
		for _, b := range r.loops[h].body {
			r.loopOf[b] = h
		}
	}
}

// addNaturalLoop computes and merges the natural loop body for a back edge
// (tail→header).
func (r *relooper) addNaturalLoop(header, tail int) {
	li, ok := r.loops[header]
	if !ok {
		li = &loopInfo{header: header}
		r.loops[header] = li
	}
	// natural loop body: reverse BFS from tail, not crossing header
	inBody := map[int]bool{header: true}
	stack := []int{tail}
	inBody[tail] = true
	preds := r.predsOf()
	for len(stack) > 0 {
		u := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, p := range preds[u] {
			if !inBody[p] {
				inBody[p] = true
				stack = append(stack, p)
			}
		}
	}
	// merge into li.body (deduplicated)
	existing := map[int]bool{}
	for _, b := range li.body {
		existing[b] = true
	}
	for b := range inBody {
		if !existing[b] {
			li.body = append(li.body, b)
			existing[b] = true
		}
	}
	sort.Ints(li.body)
}

func (r *relooper) predsOf() [][]int {
	n := len(r.c.blocks)
	preds := make([][]int, n)
	for _, bb := range r.c.blocks {
		for _, s := range bb.succs {
			preds[s] = append(preds[s], bb.id)
		}
	}
	return preds
}
