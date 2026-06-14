//go:build wangshu_p3

package wasm

// 控制流图(CFG)构造 + relooper(02-translation §3.1.7 控制流结构化)。
//
// 问题:Wasm 控制流是**结构化**的(嵌套 block/loop/if + br depth),Lua 字节码
// JMP 是**任意 pc 跳转**。relooper 把字节码 CFG 重构成嵌套 Wasm 结构。
//
// 关键事实:Lua codegen 产出的 CFG 是**可约简的**(reducible)——源语言
// (if/while/for/repeat/and/or)是结构化的,编译产物的跳转图天然可约简
// (每个循环有唯一入口,无跨循环跳入)。这让 relooper 可用相对简单的
// 「支配树 + 自然循环检测」方案,无需处理不可约简图的节点复制。
//
// 实装策略(Stackifier 风格,对 reducible CFG):
//  1. 把 Proto.Code 切成 basic block(BB)——跳转目标 / 跳转后一条是 BB 边界;
//  2. 算 BB 的支配关系,识别自然循环(回边:目标支配源);
//  3. 按支配树 DFS 生成嵌套 Wasm 结构:循环头包 loop,前向汇合点包 block,
//     前向跳 = br 到对应 block end,回边 = br 到对应 loop 头。
//
// 本文件建 CFG 结构;relooper 主算法在 relooper.go。

import (
	"sort"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// basicBlock 是一段无内部跳转的连续指令([startPC, endPC))。
type basicBlock struct {
	id      int
	startPC int32
	endPC   int32 // 开区间:[startPC, endPC)

	// 后继:fallthrough(顺序下一条)与 jumpTarget(若末指令是跳转)。
	// Lua 控制流指令的后继语义:
	//   - JMP:无条件,唯一后继 = jumpTarget
	//   - 条件(EQ/LT/LE/TEST/TESTSET/TFORLOOP):pc++ 跳过紧邻 JMP / 落入 JMP,
	//     两后继 = fallthrough(pc+1)与 fallthrough+1(被跳过的指令之后)
	//   - FORLOOP:回跳(succ=jumpTarget,回边)或落出(succ=fallthrough)
	//   - FORPREP:无条件跳到 FORLOOP(succ=jumpTarget)
	//   - RETURN / TAILCALL(末):无后继(函数出口)
	succs []int // 后继 BB id(去重、有序)
}

// cfg 是一个 Proto 的控制流图。
type cfg struct {
	proto  *bytecode.Proto
	blocks []*basicBlock
	pcToBB map[int32]int // 每个 BB 起始 pc → BB id
	entry  int           // 入口 BB id(pc=0 所在 BB)
}

// buildCFG 从 Proto.Code 构造 CFG。
func buildCFG(proto *bytecode.Proto) *cfg {
	code := proto.Code
	n := int32(len(code))

	// 1) 找所有 BB 边界(leader pc):
	//    - pc=0
	//    - 任何跳转的目标 pc
	//    - 任何跳转指令的下一条 pc
	//    - 条件指令(可能 pc++)的下一条与再下一条
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
			// 这些可能 pc++(跳过下一条);下一条与再下一条都是 leader
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

	// 2) 按 leader 切 BB
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

	// 3) 连后继边
	for _, bb := range c.blocks {
		c.linkSuccs(bb)
	}
	return c
}

// linkSuccs 给一个 BB 连后继边(按末指令语义)。
func (c *cfg) linkSuccs(bb *basicBlock) {
	code := c.proto.Code
	lastPC := bb.endPC - 1
	last := code[lastPC]
	op := bytecode.Op(last)
	n := int32(len(code))

	addSucc := func(pc int32) {
		if pc < 0 || pc >= n {
			return // 出界(函数末尾 fallthrough)= 无后继
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
		addSucc(lastPC + 1 + int32(bytecode.SBx(last))) // 回跳
		addSucc(lastPC + 1)                             // 落出
	case bytecode.EQ, bytecode.LT, bytecode.LE,
		bytecode.TEST, bytecode.TESTSET, bytecode.TFORLOOP, bytecode.LOADBOOL:
		addSucc(lastPC + 1) // 不 pc++:落下一条
		addSucc(lastPC + 2) // pc++:跳过下一条
	case bytecode.RETURN, bytecode.TAILCALL:
		// 函数出口:无后继
	default:
		addSucc(lastPC + 1) // 普通指令:fallthrough
	}
}
