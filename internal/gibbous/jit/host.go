//go:build wangshu_p4

package jit

// P4HostState 是 P4 简化形态需要从 host(crescent)调用的最小抽象接口。
//
// **依赖解环**(承 docs/design/p4-method-jit/05-system-pipeline.md §4.3 +
// gibbous/wasm/helpers.go::HostState 同款手法):p4Code.Run 需要调 host 的
// DoReturn 弹帧(因为 P4 简化形态 mmap 段不内调 host helper),但 jit 包不能
// import crescent(成环)。解法:本接口由 crescent.State 实装,wireP4 时注入。
//
// **PJ7 真接入用**:p4Code 持本接口,Run 调用方完成「值已写回 R(A)」后,
// 调本接口的 DoReturn 让 host 完成「按 nresults 移结果到 funcIdx + 弹帧 +
// 恢复 caller top」(承 gibbous_host.go::DoReturn 同款语义)。
//
// **与 P3 HostState 的关系**:P3 HostState 是 wasm helper 集(GetUpval /
// SetUpval / DoReturn / Safepoint / Arith / GetTable 等 ~25 个方法);P4 简化
// 形态当前仅需 DoReturn / SetReg / GetUpval / Arith 四个(留 PJ8+ 算术族真
// 接入 / 表 IC 真接入时扩)。
type P4HostState interface {
	// DoReturn 处理 P4 帧 RETURN A B:返回值回填到调用者期望槽 + 弹帧。
	DoReturn(base int32, pc int32, a int32, b int32) int32

	// SetReg 直接写当前帧的 R(idx) 槽位为 val(NaN-box u64)。
	SetReg(idx int32, val uint64)

	// GetUpval 读当前 closure 的 upvalue B(execute.go GETUPVAL 段同款语义)。
	//
	// 用例:P4 GETUPVAL 形态 — Run 在 mmap 段执行后调本接口取 upvalue 值,
	// 经 SetReg 写 R(retA)。
	GetUpval(base int32, b int32) uint64

	// SetUpvalFromReg 写当前 closure 的 upvalue b 为 R(a)(execute.go SETUPVAL
	// 段同款语义)。本接口把「读 R(a) + 写 upvalue」打成原子操作,避免引入
	// GetReg 通用接口(同等覆盖 NOT/SETUPVAL 等需要读寄存器再写 host 的场景)。
	//
	// 与 gibbous_host.go::State.SetUpval(base, b, val) 不同:那个签名要求
	// caller 先有 val,但 jit/P4HostState 无 GetReg 接口读 R(a)——故包装
	// 一层「带 reg 读」的对偶 helper。永不 raise。
	//
	// 用例:P4 SETUPVAL A B + RETURN A 1 形态(`function(v) upval = v end`)。
	SetUpvalFromReg(base int32, a int32, b int32)

	// GetReg 读取当前帧 R(idx) 槽位的 NaN-box u64(P4 PJ7 内 Run 完成 host
	// 依赖运算需要读寄存器的场景)。与 SetReg 对偶。
	//
	// 用例:NOT A B + RETURN A 2(`function(x) return not x end`)——Run 需
	// 读 R(B) 真假性 + SetReg(A, BoolValue(!Truthy(...)))。
	GetReg(idx int32) uint64

	// Arith 算术慢路径(ADD/SUB/MUL/DIV/MOD/POW)助手(gibbous_host.go::Arith
	// 同款签名,与 P3 helper 复用,逐字节同构于解释器 doArith)。
	//
	// 参数:
	//   - base/pc:当前帧 base 字节偏移 + 当前 pc(物化 ci.savedPC,与 P3 同款)
	//   - op:bytecode.OpCode 值(ADD=12 / SUB=13 / MUL=14 / DIV=15 / MOD=16 /
	//     POW=17 等)
	//   - b/c:RK 寄存器 / 常量索引(B/C 字段直传)
	//   - a:目标 R(A) 寄存器号(helper 内经 setReg 写)
	//
	// 返回:0=OK / 1=ERR(raise pending,enterGibbous 取走冒泡)。
	//
	// 用例:P4 ADD/SUB/MUL/... 形态 — Run 在 mmap 段执行后调本接口完成算术 +
	// 写 R(A),然后调 DoReturn 弹帧。
	Arith(base int32, pc int32, op int32, b int32, c int32, a int32) int32

	// Unm 一元负号 UNM 慢路径助手(gibbous_host.go::Unm 同款签名,逐字节同构
	// 于解释器 UNM 段慢路径:string coercion + __unm 元方法)。
	//
	// 参数:base/pc 同 Arith;b = 源寄存器号;a = 目标寄存器号(R(A))。
	//
	// 返回:0=OK / 1=ERR。用例:P4 UNM A B 形态。
	Unm(base int32, pc int32, b int32, a int32) int32

	// Len 长度运算 LEN 慢路径助手(gibbous_host.go::Len 同款签名,逐字节同构
	// 于解释器 LEN 段:string 字节长 / table border / table __len / 异类报错)。
	//
	// 参数:base/pc 同 Arith;b = 源寄存器号;a = 目标寄存器号(R(A))。
	//
	// 返回:0=OK / 1=ERR。用例:P4 LEN A B 形态。
	Len(base int32, pc int32, b int32, a int32) int32

	// NewTable 处理 NEWTABLE A B C 助手(gibbous_host.go::NewTable 同款签名,
	// 分配 + safepoint 全 helper 内,永不 raise——只可能 Go 端 OOM)。
	//
	// 参数:base/pc 同 Arith;a = 目标寄存器号;b/c = 数组段/哈希段初始大小
	// 的 Fb 编码(luac 提示)。
	//
	// 返回:0=OK / 1=ERR(理论不发生,签名保留与其它 helper 对齐)。
	// 用例:P4 NEWTABLE A B C 形态(`function() return {} end` 类)。
	NewTable(base int32, pc int32, a int32, b int32, c int32) int32

	// GetTable 处理 GETTABLE A B C 慢路径助手(gibbous_host.go::GetTable
	// 同款签名,逐字节同构于解释器 GETTABLE 段:经 icGetTable IC 缓存
	// 命中 / 哈希查表 / __index 元方法链,可 raise:attempt to index nil
	// 等)。
	//
	// 参数:base/pc 同 Arith;a = 目标寄存器号;b = 表所在寄存器;c = RK
	// 键(寄存器号或常量索引)。
	//
	// 返回:0=OK / 1=ERR。用例:P4 GETTABLE A B C 形态(`function(t, k)
	// return t[k] end` / `function(t) return t.x end` 类)。
	GetTable(base int32, pc int32, a int32, b int32, c int32) int32

	// SetTable 处理 SETTABLE A B C 慢路径助手(gibbous_host.go::SetTable
	// 同款签名,经 icSetTable IC + 哈希 + __newindex 元方法链,可 raise)。
	//
	// 参数:base/pc 同 Arith;a = 表所在寄存器;b/c = RK 键 / 值(寄存器
	// 号或常量索引)。
	//
	// 返回:0=OK / 1=ERR。用例:P4 SETTABLE A B C 形态(`function(t, k, v)
	// t[k] = v end` / `function(t) t.x = 1 end` 类——setter 形态 retB=1)。
	SetTable(base int32, pc int32, a int32, b int32, c int32) int32

	// DoGetGlobal 处理 GETGLOBAL A Bx 慢路径助手(gibbous_host.go::DoGetGlobal
	// 同款签名,经 icGetTable 在 `_G` 表上查 Consts[bx],可 raise)。
	//
	// 参数:base/pc 同 Arith;a = 目标寄存器号;bx = 常量索引(全局变量名)。
	//
	// 返回:0=OK / 1=ERR。用例:P4 GETGLOBAL A Bx 形态(`function() return
	// print end` 等)。
	DoGetGlobal(base int32, pc int32, a int32, bx int32) int32

	// DoSetGlobal 处理 SETGLOBAL A Bx 慢路径助手(gibbous_host.go::DoSetGlobal
	// 同款签名,经 icSetTable 在 `_G` 表上写 Consts[bx] = R(a),可 raise)。
	//
	// 参数:base/pc 同 Arith;a = 源寄存器号;bx = 常量索引(全局变量名)。
	//
	// 返回:0=OK / 1=ERR。用例:P4 SETGLOBAL A Bx 形态(setter retB=1)。
	DoSetGlobal(base int32, pc int32, a int32, bx int32) int32

	// Compare 处理 EQ/LT/LE 比较助手(gibbous_host.go::Compare 同款签名,
	// 经 doCompare 复刻解释器 EQ/LT/LE 段:string 比较 / __lt/__le 元方法)。
	//
	// 参数:base/pc 同 Arith;op = bytecode.OpCode(EQ=23/LT=24/LE=25);
	// b/c = RK 寄存器 / 常量索引(B/C 字段直传)。
	//
	// 返回:packed - bit0=比较结果(0=false / 1=true), bit1=错误标志
	// (2=ERR pending,enterGibbous 取走冒泡)。
	//
	// 用例:P4 EQ/LT/LE + JMP + LOADBOOL×2 + RETURN 折叠形态
	// (`function(x) return x == 1 end` 类——经 packed bit0 vs cmpA 折成
	// BoolValue 直接写 R(A))。
	Compare(base int32, pc int32, op int32, b int32, c int32) int32

	// ArenaBaseAddr 返回 arena `[]byte` 起点的 uintptr(承 05 §3.3)。
	//
	// 用例:PJ2 完整投机模板——mmap 段经 r15+offset 读 arenaBase 字段后
	// 经字节级 movsd 直接读/写值栈槽位,跳过 host 接口 round-trip。
	// PJ7 简化形态不调用本接口(mmap 段是 dummy)。
	ArenaBaseAddr() uintptr

	// ValueStackBaseAddr 返回当前帧 R0 的字节地址(承 05 §3.3 + 06 §4.1
	// rbx = valueStackBase)。
	//
	// 参数 base 是当前帧 R0 字节偏移(承 enterGibbous 计算 baseByte =
	// (stackBaseW + ci.base) * 8,与 DoReturn 传入的 base 同语义)。
	//
	// 返回:arena.Words().bytePtr + base —— 这是 R0 在 Go 进程虚地址空间
	// 中的真字节地址。mmap 段读 r15+offset 拿本字段后经 movsd
	// [valueStackBase + reg*8] 寻址 R(reg)。
	//
	// **arena 重定位风险**:arena grow 时 Words() 会重新分配,本字段会
	// stale。承 05 §5 arena base 重载协议:grow 只在分配慢路径(出 JIT
	// 世界)发生,JIT 内联 bump 越界即出去——回来后从 jitContext 重载
	// base。PJ2 完整版接入此协议;PJ7 简化形态尚不调用本接口。
	ValueStackBaseAddr(base int32) uintptr
}

// SetHostState 把 host(crescent)抽象注入本 Compiler。
//
// **per-Compiler 单例**(承 wireP4 调用契约):每个 State 一份 *Compiler,本
// 方法在 wireP4 单 goroutine 内调一次;后续 Compile 产出 p4Code 时把 Compiler
// 的 hostState 复制到 p4Code 字段;p4Code.Run 用自己持有的 hostState(per-
// p4Code 单 writer-then-reader,无并发 write)。
//
// 这避免了 package-level global hostState 的多 State 并发写 race(V18 -race
// 友好,承 design-claims-vs-codebase-physics 纪律——每发现一次 race 修一次)。
func (c *Compiler) SetHostState(h P4HostState) {
	c.hostState = h
}
