//go:build wangshu_p3

package wasm

// 结构化生成层(02-translation §3.3/§3.5,PW4 relooper):把 reducible CFG
// 的多 BB 重构成嵌套 Wasm block/loop + br depth。
//
// 算法(LLVM WebAssemblyCFGStackify / Binaryen Stackify 路线,对 reducible
// CFG 正确):
//   Phase A: layoutOrder —— 循环连续的发射顺序(CFGSort)。原始 RPO 可能把
//            循环体被循环外 BB 劈开,无法直接套 loop;DFS 后序时「先离开当前
//            循环的后继、后留在循环内的后继」,使循环体在发射序连续。
//   Phase B: computeScopes —— 算每个 loop header / 前向汇合 BB 的作用域区间
//            [begin,end](发射序位置),保证作用域两两要么不交要么嵌套。
//   Phase C: emit —— 作用域栈驱动:逐 BB 发射,边界处先关(end)后开(loop/
//            block),BB 末终结边经 br depth(目标作用域在栈中的相对位置)。
//
// **安全阀**:任何本层无法正确结构化的形态(不可约简 / br 目标不在栈上 /
// 作用域非法嵌套)一律返回 error → Compile 转 CompileError → P2 fallback 解释器
// (byte-equal 平凡成立)。部分实现因此始终安全——只是升层的 Proto 更少。

import (
	"fmt"
	"sort"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// scopeKind 作用域种类。
type scopeKind int

const (
	scLoop  scopeKind = iota // loop:br 到它 = 回跳到起点(continue/回边)
	scBlock                  // block:br 到它 = 跳到 end 之后(前向跳/汇合)
	scIf                     // if:占一层 br 深度(无 target,不作为 br 目标)
)

// scope 是一个作用域区间(发射序位置 [begin,end] 闭区间)。
type scope struct {
	kind   scopeKind
	target int // loop: header BB id;block: 汇合 BB id;if: 无意义(-1)
	begin  int // 发射序起始位置(loop/block 在此位置前发)
	end    int // 发射序结束位置(end 在此位置后发)
}

// structPlan 是结构化分析产物(layoutOrder + scopes),供发射层消费。
type structPlan struct {
	order  []int   // 发射顺序(BB id)
	pos    []int   // BB id → 发射序位置
	begins [][]int // 位置 → 该位置开始的 scope 下标(外层先,见 computeScopes)
	scopes []scope // 全部 loop/block 作用域(if 不在此,运行期压栈)
}

// layoutOrder 产循环连续的发射顺序(Phase A)。
func (r *relooper) layoutOrder() ([]int, []int) {
	n := len(r.c.blocks)
	visited := make([]bool, n)
	var post []int
	var dfs func(u int)
	dfs = func(u int) {
		visited[u] = true
		lu := r.loopOf[u] // u 所属最内层循环 header(-1 = 不在循环内)
		succs := append([]int(nil), r.c.blocks[u].succs...)
		// 排序键:① 留在 lu 的后继排后(后访问 → 后序靠前 → RPO 靠后 = 循环体
		// 连续紧跟 header)② id 升序(确定性)。
		sort.SliceStable(succs, func(i, j int) bool {
			si, sj := r.staysIn(succs[i], lu), r.staysIn(succs[j], lu)
			if si != sj {
				return !si // 离开者(false)先访问
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

// staysIn 判 s 是否属于循环 h 的循环体(h=-1 恒 false)。
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

// isReducible O(E) 可约简性检测:可达子图上每条「逆向边」(rpoIndex[v] ≤
// rpoIndex[u])必须是回边(v 支配 u);否则即多入口循环(不可约简)→ 拒。
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
				// 逆向边(含自环):必须是回边(目标支配源)
				if !r.dominates(s, bb.id) {
					return false
				}
			}
		}
	}
	return true
}

// computeScopes 算 loop / block 作用域并定开启顺序(Phase B)。
//
// 返回 error 表示出现本层无法安全结构化的形态(应 fallback)。
func (r *relooper) computeScopes(order, pos []int) ([]scope, [][]int, error) {
	n := len(r.c.blocks)
	var scopes []scope

	// --- loop 作用域:每 header 覆盖 [pos[h], max body pos] ---
	for h, li := range r.loops {
		if pos[h] < 0 {
			continue // 不可达(理论上不会,loops 只含可达)
		}
		end := pos[h]
		for _, b := range li.body {
			if pos[b] > end {
				end = pos[b]
			}
		}
		scopes = append(scopes, scope{kind: scLoop, target: h, begin: pos[h], end: end})
	}

	// --- block 作用域:有「非 fallthrough 前向入边」的汇合 BB 需要 block ---
	// 前向入边 u→d:pos[u] < pos[d] 且非自然 fallthrough(pos[u]+1==pos[d] 且
	// d 是 u 的顺序后继)。
	preds := r.predsOf()
	for _, d := range order {
		var fwd []int
		for _, p := range preds[d] {
			if pos[p] < 0 || pos[p] >= pos[d] {
				continue // 非前向(回边/同位)
			}
			// 自然 fallthrough:p 紧邻 d 之前,且 p 的终结不是「跳走」(由发射层
			// 决定能否 fallthrough);此处保守地把「pos[p]+1==pos[d]」视作可能
			// fallthrough,但只有当 d 是 p 唯一前向去向时才省 block。多前驱汇合
			// 仍需 block。
			fwd = append(fwd, p)
		}
		if len(fwd) == 0 {
			continue
		}
		// 需要 block:覆盖 [minPredPos, pos[d]-1],br 到它落到 d。
		begin := pos[d]
		for _, p := range fwd {
			if pos[p] < begin {
				begin = pos[p]
			}
		}
		if begin >= pos[d] {
			continue // 无前向跨越(仅 fallthrough),无需 block
		}
		// 嵌套修复:若已有作用域 s 的 begin 落在 (begin, pos[d]) 内但 end ≥
		// pos[d],把 begin 前移到 s.begin 之前形成包含(保证两两不交或嵌套)。
		changed := true
		for changed {
			changed = false
			for _, s := range scopes {
				if s.begin > begin && s.begin < pos[d] && s.end >= pos[d] {
					begin = s.begin - 1
					changed = true
				}
			}
		}
		if begin < 0 {
			return nil, nil, fmt.Errorf("p4 relooper: block scope begin underflow for BB %d", d)
		}
		scopes = append(scopes, scope{kind: scBlock, target: d, begin: begin, end: pos[d] - 1})
	}

	// --- 校验作用域两两不交或嵌套 ---
	for i := range scopes {
		for j := i + 1; j < len(scopes); j++ {
			if overlapImproper(scopes[i], scopes[j]) {
				return nil, nil, fmt.Errorf("p4 relooper: improper scope overlap %v vs %v", scopes[i], scopes[j])
			}
		}
	}

	// --- begins 索引:位置 → scope 下标,外层先开(end 大者先;同 end 则 begin
	// 小者先;再以 block 在 loop 外、target 远者外 兜底确定性)---
	begins := make([][]int, n)
	for idx, s := range scopes {
		begins[s.begin] = append(begins[s.begin], idx)
	}
	for p := range begins {
		ids := begins[p]
		sort.SliceStable(ids, func(a, b int) bool {
			sa, sb := scopes[ids[a]], scopes[ids[b]]
			if sa.end != sb.end {
				return sa.end > sb.end // end 大者外层,先开
			}
			// 同区间:汇合点 pos 大者(更晚 merge)为外层先开
			return pos[sa.target] > pos[sb.target]
		})
	}
	return scopes, begins, nil
}

// overlapImproper 判两作用域是否「部分交叠」(非不交、非嵌套)。
func overlapImproper(a, b scope) bool {
	// 不交
	if a.end < b.begin || b.end < a.begin {
		return false
	}
	// a 含 b 或 b 含 a
	if (a.begin <= b.begin && b.end <= a.end) || (b.begin <= a.begin && a.end <= b.end) {
		return false
	}
	return true // 部分交叠
}

// buildStructPlan 跑 Phase A+B 产结构化计划(translate 调用)。
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

// emitStructured 是 Phase C:作用域栈驱动发射(Compiler 方法,调 emitOpcode)。
func (c *Compiler) emitStructured(em *emitter, proto *bytecode.Proto, cfg *cfg, plan *structPlan) error {
	var stack []scope
	for i, bb := range plan.order {
		// ① 关闭在位置 i 之前结束的作用域(栈顶=最内,先关)。
		for len(stack) > 0 {
			top := stack[len(stack)-1]
			if top.kind == scIf {
				// if 由比较/FOR 发射时自行配对 end,不在此关。
				break
			}
			if top.end < i {
				em.end()
				stack = stack[:len(stack)-1]
			} else {
				break
			}
		}
		// ② 打开在位置 i 开始的作用域(begins 已排外层先)。
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
		// ③ 发射 BB 本体 + 终结边。
		if err := c.emitBlockBody(em, proto, cfg, plan, bb, &stack); err != nil {
			return err
		}
	}
	// flush 剩余作用域。
	for len(stack) > 0 {
		if stack[len(stack)-1].kind != scIf {
			em.end()
		}
		stack = stack[:len(stack)-1]
	}
	return nil
}

// brDepth 算 dst 对应作用域(loop header 或 block 汇合点)在栈中的相对深度。
// Wasm:br 0 = 最内层(栈顶);返回 (depth, true) 或 (0, false)。
func brDepth(stack []scope, dst int, kind scopeKind) (uint32, bool) {
	for t := len(stack) - 1; t >= 0; t-- {
		if stack[t].kind == kind && stack[t].target == dst {
			return uint32(len(stack) - 1 - t), true
		}
	}
	return 0, false
}

// emitEdge 发一条 CFG 边 src→dst:能 fallthrough(dst 是发射序下一 BB 且无中间
// 作用域边界)就不发指令;否则 br 到对应作用域(回边=loop / 前向=block)。
func (c *Compiler) emitEdge(em *emitter, plan *structPlan, stack []scope, srcBB, dst int) error {
	// 回边:dst 是栈上某 loop header。
	if d, ok := brDepth(stack, dst, scLoop); ok {
		em.br(d)
		return nil
	}
	// 前向汇合:dst 是栈上某 block 的 end 之后。
	if d, ok := brDepth(stack, dst, scBlock); ok {
		em.br(d)
		return nil
	}
	// fallthrough:dst 恰是发射序下一个 BB(自然落下,不发指令)。
	if plan.pos[srcBB]+1 == plan.pos[dst] {
		return nil
	}
	return fmt.Errorf("p4 relooper: edge %d→%d not on scope stack and not fallthrough", srcBB, dst)
}
