//go:build wangshu_p3

package wasm

// relooper:把 reducible CFG 重构成嵌套 Wasm 结构化控制流
// (02-translation §3.1.7)。
//
// 算法(对 reducible CFG,Lua codegen 产物天然 reducible):
//   1. 逆后序(RPO)给 BB 编号 —— 保证支配者在被支配者之前;
//   2. 算支配树(Cooper-Harvey-Kennedy 迭代不动点);
//   3. 识别自然循环(回边:succ 支配 pred);
//   4. Stackifier 风格生成:按 RPO 线性发射 BB,用 Wasm block/loop 嵌套 +
//      br/br_if 表达跳转:
//        - 循环头前开 loop;循环回边 = br 到 loop 标签;
//        - 前向跳目标前开 block;前向跳 = br 到 block end 标签;
//        - 嵌套用「作用域栈」管理,br depth 由目标标签在栈中的相对位置算。
//
// 本实现采用 WebAssembly 经典的「scope stack + 按需放置 block/loop」方案
// (参考 Binaryen Stackify / wasm-stackifier),对 reducible CFG 正确。

import "sort"

// loopInfo 描述一个自然循环。
type loopInfo struct {
	header int   // 循环头 BB id
	body   []int // 循环体 BB id 集合(含 header)
}

// relooper 持有 CFG 分析结果。
type relooper struct {
	c        *cfg
	rpo      []int             // 逆后序的 BB id 列表
	rpoIndex []int             // BB id → 其在 rpo 中的位置
	idom     []int             // 直接支配者(BB id → BB id;entry 的 idom = 自身)
	loops    map[int]*loopInfo // header BB id → loop
	loopOf   []int             // BB id → 所属最内层循环 header(-1 = 不在任何循环)
}

// analyzeRelooper 跑完整分析(RPO + 支配 + 循环)。
func analyzeRelooper(c *cfg) *relooper {
	r := &relooper{c: c}
	r.computeRPO()
	r.computeDominators()
	r.detectLoops()
	return r
}

// computeRPO 逆后序 DFS。
func (r *relooper) computeRPO() {
	n := len(r.c.blocks)
	visited := make([]bool, n)
	var post []int
	var dfs func(int)
	dfs = func(u int) {
		visited[u] = true
		// 后继按 id 升序访问,保证确定性
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
	// RPO = 后序反转
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

// computeDominators Cooper-Harvey-Kennedy 迭代算法。
func (r *relooper) computeDominators() {
	n := len(r.c.blocks)
	r.idom = make([]int, n)
	for i := range r.idom {
		r.idom[i] = -1
	}
	r.idom[r.c.entry] = r.c.entry

	// preds:每个 BB 的前驱
	preds := make([][]int, n)
	for _, bb := range r.c.blocks {
		for _, s := range bb.succs {
			preds[s] = append(preds[s], bb.id)
		}
	}

	changed := true
	for changed {
		changed = false
		// 按 RPO(跳过 entry)
		for _, u := range r.rpo {
			if u == r.c.entry {
				continue
			}
			newIdom := -1
			for _, p := range preds[u] {
				if r.idom[p] == -1 {
					continue // 前驱还没处理
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

// intersect 求两个 BB 在支配树上的最近公共支配者(用 RPO 序号往上爬)。
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

// dominates 判 a 是否支配 b(沿 idom 链上爬)。
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

// detectLoops 识别自然循环:回边 (u→v) 满足 v 支配 u。
// 循环体 = 能不经 v 到达 u 的所有节点 + v。
func (r *relooper) detectLoops() {
	r.loops = make(map[int]*loopInfo)
	n := len(r.c.blocks)
	r.loopOf = make([]int, n)
	for i := range r.loopOf {
		r.loopOf[i] = -1
	}

	for _, bb := range r.c.blocks {
		for _, s := range bb.succs {
			// 回边:s 支配 bb(s 是循环头,bb→s 是回跳)
			if r.dominates(s, bb.id) {
				r.addNaturalLoop(s, bb.id)
			}
		}
	}

	// 标记每个 BB 所属最内层循环(loop body 越小越内层)
	// 按 body 大小升序赋值,小循环后赋(覆盖)→ 最内层胜出
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

// addNaturalLoop 给回边 (tail→header) 计算自然循环体并合并。
func (r *relooper) addNaturalLoop(header, tail int) {
	li, ok := r.loops[header]
	if !ok {
		li = &loopInfo{header: header}
		r.loops[header] = li
	}
	// 自然循环体:从 tail 反向 BFS,不越过 header
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
	// 合并进 li.body(去重)
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
