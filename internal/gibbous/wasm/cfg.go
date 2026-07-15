//go:build wangshu_p3

package wasm

// Control-flow graph (CFG) construction + relooper (02-translation §3.1.7
// control-flow structuring).
//
// Problem: Wasm control flow is **structured** (nested block/loop/if + br
// depth), whereas Lua bytecode JMP is an **arbitrary pc jump**. The relooper
// reshapes the bytecode CFG into nested Wasm structures.
//
// Key fact: the CFG produced by Lua codegen is **reducible** — the source
// language (if/while/for/repeat/and/or) is structured, so the compiled jump
// graph is naturally reducible (each loop has a single entry, no cross-loop
// jumps into a loop body). This lets the relooper use the relatively simple
// "dominator tree + natural-loop detection" approach, with no need for node
// duplication as an irreducible graph would require.
//
// Implementation strategy (Stackifier style, for a reducible CFG):
//  1. Split Proto.Code into basic blocks (BB) — a jump target / the
//     instruction after a jump is a BB boundary;
//  2. Compute BB dominance, identify natural loops (back edge: target
//     dominates source);
//  3. Generate nested Wasm structures by DFS over the dominator tree: wrap
//     loop headers in loop, wrap forward join points in block; a forward jump
//     = br to the corresponding block end, a back edge = br to the
//     corresponding loop header.
//
// This file builds the CFG structure; the main relooper algorithm lives in
// relooper.go.

import (
	"sort"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// basicBlock is a run of instructions with no internal jumps ([startPC, endPC)).
type basicBlock struct {
	id      int
	startPC int32
	endPC   int32 // half-open range: [startPC, endPC)

	// Successors: fallthrough (the next instruction in order) and jumpTarget
	// (if the last instruction is a jump).
	// Successor semantics of Lua control-flow instructions:
	//   - JMP: unconditional, single successor = jumpTarget
	//   - conditional (EQ/LT/LE/TEST/TESTSET/TFORLOOP): pc++ skips the
	//     adjacent JMP / falls into the JMP, two successors = fallthrough
	//     (pc+1) and fallthrough+1 (after the skipped instruction)
	//   - FORLOOP: jump back (succ=jumpTarget, back edge) or fall out
	//     (succ=fallthrough)
	//   - FORPREP: unconditional jump to FORLOOP (succ=jumpTarget)
	//   - RETURN / TAILCALL (as last): no successor (function exit)
	succs []int // successor BB ids (deduped, ordered)
}

// cfg is the control-flow graph of a single Proto.
type cfg struct {
	proto  *bytecode.Proto
	blocks []*basicBlock
	pcToBB map[int32]int // start pc of each BB → BB id
	entry  int           // entry BB id (the BB containing pc=0)
}

// buildCFG constructs the CFG from Proto.Code.
func buildCFG(proto *bytecode.Proto) *cfg {
	code := proto.Code
	n := int32(len(code))

	// 1) Find all BB boundaries (leader pcs):
	//    - pc=0
	//    - the target pc of any jump
	//    - the pc following any jump instruction
	//    - the next and the one-after-next pc of a conditional instruction
	//      (which may pc++)
	leaders := map[int32]bool{0: true}
	for pc := int32(0); pc < n; pc++ {
		ins := code[pc]
		op := bytecode.Op(ins)
		switch op {
		case bytecode.JMP:
			tgt := pc + 1 + int32(bytecode.SBx(ins))
			leaders[tgt] = true
			if pc+1 < n {
				leaders[pc+1] = true
			}
		case bytecode.FORLOOP, bytecode.FORPREP:
			tgt := pc + 1 + int32(bytecode.SBx(ins))
			leaders[tgt] = true
			if pc+1 < n {
				leaders[pc+1] = true
			}
		case bytecode.EQ, bytecode.LT, bytecode.LE,
			bytecode.TEST, bytecode.TESTSET, bytecode.TFORLOOP,
			bytecode.LOADBOOL:
			// These may pc++ (skip the next instruction); both the next and
			// the one-after-next are leaders
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

	// 2) Split into BBs by leader
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

	// 3) Link successor edges
	for _, bb := range c.blocks {
		c.linkSuccs(bb)
	}
	return c
}

// reachableBlocks does a BFS from entry to find the set of reachable BB ids.
//
// After each RETURN, Lua codegen appends a "fallback RETURN A 1" (returns 0
// values) as dead code — it makes the pc following a RETURN a leader, carving
// out an **unreachable** BB. When deciding "single straight-line BB", only
// reachable BBs must be counted (dead-code blocks never execute and do not
// affect translation correctness).
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

// linkSuccs links a BB's successor edges (per its last instruction's semantics).
func (c *cfg) linkSuccs(bb *basicBlock) {
	code := c.proto.Code
	lastPC := bb.endPC - 1
	last := code[lastPC]
	op := bytecode.Op(last)
	n := int32(len(code))

	addSucc := func(pc int32) {
		if pc < 0 || pc >= n {
			return // out of bounds (fallthrough past function end) = no successor
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
		addSucc(lastPC + 1 + int32(bytecode.SBx(last))) // jump back
		addSucc(lastPC + 1)                             // fall out
	case bytecode.EQ, bytecode.LT, bytecode.LE,
		bytecode.TEST, bytecode.TESTSET, bytecode.TFORLOOP:
		addSucc(lastPC + 1) // no pc++: fall to next instruction
		addSucc(lastPC + 2) // pc++: skip the next instruction
	case bytecode.LOADBOOL:
		// LOADBOOL skip semantics are compile-time fixed by the C field
		// (Lua reference manual: "if C, then pc++"). Comparison opcodes
		// above branch at runtime; LOADBOOL does not. Emitting both succs
		// here makes the BB look multi-exit to the translator, which then
		// rejects it via translate.go default branch ("unexpected 2 succs
		// after LOADBOOL"). Pick the single live edge by C.
		if bytecode.C(last) != 0 {
			addSucc(lastPC + 2) // C != 0: always pc++ at runtime
		} else {
			addSucc(lastPC + 1) // C == 0: fall to next instruction
		}
	case bytecode.RETURN, bytecode.TAILCALL:
		// function exit: no successor
	default:
		addSucc(lastPC + 1) // ordinary instruction: fallthrough
	}
}
