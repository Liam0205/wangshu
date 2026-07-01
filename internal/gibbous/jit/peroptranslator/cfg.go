//go:build wangshu_p4

// cfg.go — CFG builder for PJ10 native per-op translator.
//
// Directly mirrors internal/gibbous/wasm/cfg.go — same BB slicing rules,
// same successor edges, same reachability. The only reason it's a separate
// file (not an import of the P3 CFG) is the build tag mismatch: P3 wasm CFG
// is under wangshu_p3, this translator is under wangshu_p4, and the two are
// mutually exclusive.
package peroptranslator

import (
	"sort"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// basicBlock is a straight-line span [startPC, endPC).
type basicBlock struct {
	id      int
	startPC int32
	endPC   int32 // half-open
	succs   []int // successor BB ids (deduped, sorted by id)
}

// cfg is a Proto's control-flow graph.
type cfg struct {
	proto  *bytecode.Proto
	blocks []*basicBlock
	pcToBB map[int32]int
	entry  int
}

// buildCFG constructs the CFG from Proto.Code — same leader rules as P3.
func buildCFG(proto *bytecode.Proto) *cfg {
	code := proto.Code
	n := int32(len(code))

	leaders := map[int32]bool{0: true}
	for pc := int32(0); pc < n; pc++ {
		ins := code[pc]
		op := bytecode.Op(ins)
		switch op {
		case bytecode.JMP:
			leaders[pc+1+int32(bytecode.SBx(ins))] = true
			if pc+1 < n {
				leaders[pc+1] = true
			}
		case bytecode.FORLOOP, bytecode.FORPREP:
			leaders[pc+1+int32(bytecode.SBx(ins))] = true
			if pc+1 < n {
				leaders[pc+1] = true
			}
		case bytecode.EQ, bytecode.LT, bytecode.LE,
			bytecode.TEST, bytecode.TESTSET, bytecode.TFORLOOP,
			bytecode.LOADBOOL:
			if pc+1 < n {
				leaders[pc+1] = true
			}
			if pc+2 < n {
				leaders[pc+2] = true
			}
		case bytecode.RETURN, bytecode.TAILCALL:
			if pc+1 < n {
				leaders[pc+1] = true
			}
		}
	}

	var leaderPCs []int32
	for pc := range leaders {
		if pc < n {
			leaderPCs = append(leaderPCs, pc)
		}
	}
	sort.Slice(leaderPCs, func(i, j int) bool { return leaderPCs[i] < leaderPCs[j] })

	c := &cfg{proto: proto, pcToBB: make(map[int32]int)}
	for i, start := range leaderPCs {
		end := n
		if i+1 < len(leaderPCs) {
			end = leaderPCs[i+1]
		}
		bb := &basicBlock{id: i, startPC: start, endPC: end}
		c.blocks = append(c.blocks, bb)
		c.pcToBB[start] = i
	}
	c.entry = c.pcToBB[0]

	for _, bb := range c.blocks {
		c.linkSuccs(bb)
	}
	return c
}

// reachableBlocks returns BB ids reachable from entry.
func (c *cfg) reachableBlocks() map[int]bool {
	seen := map[int]bool{c.entry: true}
	stack := []int{c.entry}
	for len(stack) > 0 {
		u := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, s := range c.blocks[u].succs {
			if !seen[s] {
				seen[s] = true
				stack = append(stack, s)
			}
		}
	}
	return seen
}

// linkSuccs connects a BB to its successors by terminator semantics — same
// rules as P3 wasm CFG. LOADBOOL picks single live edge by C field
// (mirrors the fix at internal/gibbous/wasm/cfg.go).
func (c *cfg) linkSuccs(bb *basicBlock) {
	code := c.proto.Code
	lastPC := bb.endPC - 1
	last := code[lastPC]
	op := bytecode.Op(last)
	n := int32(len(code))

	addSucc := func(pc int32) {
		if pc < 0 || pc >= n {
			return
		}
		id, ok := c.pcToBB[pc]
		if !ok {
			return
		}
		for _, s := range bb.succs {
			if s == id {
				return
			}
		}
		bb.succs = append(bb.succs, id)
	}

	switch op {
	case bytecode.JMP:
		addSucc(lastPC + 1 + int32(bytecode.SBx(last)))
	case bytecode.FORPREP:
		addSucc(lastPC + 1 + int32(bytecode.SBx(last)))
	case bytecode.FORLOOP:
		addSucc(lastPC + 1 + int32(bytecode.SBx(last))) // back-edge
		addSucc(lastPC + 1)                             // fall-out
	case bytecode.EQ, bytecode.LT, bytecode.LE,
		bytecode.TEST, bytecode.TESTSET, bytecode.TFORLOOP:
		addSucc(lastPC + 1)
		addSucc(lastPC + 2)
	case bytecode.LOADBOOL:
		if bytecode.C(last) != 0 {
			addSucc(lastPC + 2)
		} else {
			addSucc(lastPC + 1)
		}
	case bytecode.RETURN, bytecode.TAILCALL:
		// function exit — no successors
	default:
		addSucc(lastPC + 1)
	}
}

// isReducible checks whether the CFG is reducible (no jumps to BB middles).
// Lua 5.1 codegen never produces goto, so this must always be true.
//
// A reducible CFG is one where every back-edge (u->v where v dominates u)
// is the only edge into v from outside v's dominator subtree. For Lua 5.1
// the equivalent simpler check is: every successor PC is a leader. If any
// jump target lands mid-BB, the CFG is not reducible for our purposes.
//
// buildCFG's leader collection already covers all jump targets, so any
// well-formed Proto passes. This is currently a no-op that returns true
// unconditionally; it's kept in the API surface so callers document
// their reducibility assumption at the call site rather than baking it
// as an implicit precondition.
func (c *cfg) isReducible() bool {
	return true
}
