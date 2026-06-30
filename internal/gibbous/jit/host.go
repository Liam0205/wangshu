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

	// Concat 字符串拼接 CONCAT A B C 慢路径助手(gibbous_host.go::Concat
	// 同款签名,逐字节同构于解释器 CONCAT 段:R(A) := R(B) .. R(B+1) .. ..
	// R(C),含 __concat 元方法 + 数字 / 字符串混拼;可 raise)。
	//
	// 参数:base/pc 同 Arith;a = 目标寄存器号;b/c = CONCAT 范围首尾
	// (R(B..C) 闭区间),helper 内经 doConcat 完成连接 + setReg。
	//
	// 返回:0=OK / 1=ERR。用例:P4 CONCAT A B C 形态(`function(x, y)
	// return x .. y end` 类)。
	Concat(base int32, pc int32, a int32, b int32, c int32) int32

	// Eq EQ 相等比较慢路径(gibbous_host.go::Eq 同款签名,经 doCompare EQ
	// 分支:raw 等检查 + __eq 元方法;可 raise)。
	//
	// 参数:base/pc 同 Arith;b/c = 操作数(EQ B C,RK 编码取寄存器或常量)。
	//
	// 返回:packed,bit0 = 比较结果(0/1),bit1 = 错误标志(2)。
	Eq(base int32, pc int32, b int32, c int32) int32

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

	// ForPrep 处理 FORPREP A sBx 助手(gibbous_host.go::ForPrep 同款签名,
	// 三槽校验 + coercion + 预减,复用 P1 execute.go FORPREP 段)。
	//
	// 用例:P4 PJ3 reg-limit FORLOOP 形态 — IsNumber guard 失败时降级调
	// host.ForPrep + host.ForLoop(模板 deopt 路径,byte-equal 解释器)。
	//
	// 参数:base/pc 同 Arith;a = FORPREP 的 A 字段(R(A)..R(A+2) = init/
	// limit/step 三槽)。
	//
	// 返回:0=OK / 1=ERR(raise pending,'for' init/limit/step must be a number)。
	ForPrep(base int32, pc int32, a int32) int32

	// CallBaseline 处理 CALL A B C 的 baseline 同步路径(承
	// docs/design/p4-method-jit/05-system-pipeline.md §4.3,**绕过 P3 R3 indirect
	// 直调哨兵协议**——简化版只走 baseline doCall 分派 + 同步驱动被调帧到完
	// 成,免引入段内 call_indirect 通道。
	//
	// 参数 base/pc 同 Arith;a/b/c 是 CALL A B C 三字段:
	//   - a = 被调函数寄存器号(R(A));参数从 R(A+1..A+B-1)
	//   - b = 参数计数 + 1(B=0 表示「到 top」,B=1 表 0 参数,B=N 表 N-1 参数)
	//   - c = 返回值计数 + 1(C=0 表「到 top」,C=1 表 0 返回值,C=N 表 N-1 返回值)
	//
	// 返回:0=OK(被调帧已完成 + 结果已落 R(A..A+C-2),caller 帧仍活)/
	//      1=ERR(pendingErr 已置 → 上层 ERR 冒泡)。
	//
	// **与 P3 wasm 端 DoCall 的差异**:DoCall 返 i64 三态(<0/odd/even)用于
	// wasm 端 call_indirect 直调分派;P4 PJ5 简化形态没有 wasm-level 段内
	// indirect 通道,所以 host 端**必须**走 baseline doCall(host/crescent/__call/
	// 全形态 gibbous 一律同步跑完),不进 tryIndirectCallee 快路径。
	//
	// **简化形态用例**(`function(g) g() end` 类):Run 端 prelude 路径调
	// 本接口完成调用 + 后续 DoReturn 弹帧。byte-equal P1 解释器 doCall 路径。
	CallBaseline(base int32, pc int32, a int32, b int32, c int32) int32

	// TailCall 处理 TAILCALL A B C 的 baseline 同步路径(承
	// docs/design/p4-method-jit/05-system-pipeline.md §4.3 + p1-interpreter/
	// 05-interpreter-loop.md §8.4 — 尾调用复用帧 + executeFrom 同步驱动 callee 链)。
	//
	// 参数 base/pc 同 CallBaseline;a/b/c 是 TAILCALL A B C 三字段(luac 编 C=0):
	//   - a = 被调函数寄存器号(R(A));参数从 R(A+1..A+B-1)
	//   - b = 参数计数 + 1(B=0 表「到 top」,B=1 表 0 参数,B=N 表 N-1 参数)
	//   - c = 返回值计数 + 1(TAILCALL 永远 C=0,luac 编 stmtReturn 强制写 0
	//     表「到 top」,与尾随 RETURN B=0 衔接)
	//
	// 返回(三态分支,与 crescent.State.TailCall 同款):
	//   - 0 = Lua 尾调用完成。caller 帧已被 callee 帧替换 + executeFrom 同步驱动
	//     callee 链到完成 + nresults 写回上层 funcIdx。Run 端**跳过 DoReturn**
	//     (本帧已弹),直接 return 0。
	//   - 1 = ERR(raise pending → 上层 ERR 冒泡)。
	//   - 2 = host 尾调用。结果已落 R(A..A+nrets-1),G 帧未弹。Run 端**正常调
	//     DoReturn**(对位 luac 编的尾随 dead RETURN A B=0,nret 到 top)。
	//
	// **简化形态用例**(`function(g) return g() end` / `function() return f() end`
	// 等):Run 端 prelude 路径调本接口完成 + 三态分支(byte-equal P1 解释器
	// doTailCall 路径)。
	TailCall(base int32, pc int32, a int32, b int32, c int32) int32

	// Self 处理 SELF A B C 助手(gibbous_host.go::Self 同款签名,逐字节同构
	// 解释器 SELF 段:R(A+1)=R(B) self + R(A)=R(B)[RK(C)] method,经
	// icGetTable IC + 哈希 + __index 元方法链,可 raise:attempt to index nil 等)。
	//
	// 参数:
	//   - base/pc:当前帧 base 字节偏移 + 当前 pc(物化 ci.savedPC,与解释器同款)
	//   - a:SELF.A(目标寄存器:method 结果到 R(A),self 到 R(A+1))
	//   - b:SELF.B(receiver 寄存器号 0-255)
	//   - c:SELF.C(RK 编码 0-511,常量 256 偏移)
	//
	// 返回:0=OK / 1=ERR(raise pending,enterGibbous 取走冒泡)。
	//
	// 用例:P4 PJ5 SELF + CALL/TAILCALL inline 形态(`obj:method(args)` 类)。
	// Run 端 prelude 路径调 host.Self 装 method/self,然后调 CallBaseline /
	// TailCall 完成 byte-equal P1 doCall 分派。
	Self(base int32, pc int32, a int32, b int32, c int32) int32

	// ArenaBaseAddr 返回 arena `[]byte` 起点的 uintptr(承 05 §3.3)。	//
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

	// CIDepthHostAddr 返回 thread.ciDepth 镜像字的 host 字节地址(承 §9.20
	// Option B Spike 1)。
	//
	// **复用 P3 PW10 Stage 1a 镜像字**(crescent.State.ciDepthRef):同一镜像字
	// crescent 端经 setCIDepth 写入,P4 mmap 段经 host addr (uintptr) 读 / inc / dec。
	// 返回 = arena.Words().bytePtr + (st.ciDepthRef bytes)。
	//
	// **arena 重定位风险**:同 ArenaBaseAddr,arena grow 出 JIT 世界后回来从
	// jitContext 重载;Spike 1 阶段每次 Run 入口注入。
	CIDepthHostAddr() uintptr

	// CISegBaseHostAddr 返回 CI 段当前字节基址镜像字的 host 字节地址(承 §9.20)。
	//
	// **复用 P3 PW10 Stage 2 镜像字**(crescent.State.ciSegBaseRef):CI 段可
	// 重定位,mmap 段经此镜像字解引出当前 CI 段基址,然后算 CallInfo[depth]
	// 帧地址(基址 + depth*40)。
	CISegBaseHostAddr() uintptr

	// TopHostAddr 返回 thread.top 镜像字的 host 字节地址(承 §9.20)。
	//
	// **复用 P3 PW10 Stage 1a 镜像字**(crescent.State.topRef):top 是栈槽索引,
	// enterLuaFrame 设 callee 帧顶时 mmap 段写入(top = base + MaxStack)。
	TopHostAddr() uintptr

	// ExecuteCalleeFromInlineFrame Spike 1 Step C-1 helper API(承 §9.20.7
	// 真实装拆解 + §9.20.9 trampoline exit-resume 协议 commit-2 接口 +
	// commit-5l 签名修正:callA 替代 retA,SELF + CALL 形态下 method 在
	// R(callA),callA 是 callee 槽位识别的正确字段)。
	//
	// **前置条件**(caller mmap 段必须保证):
	//   - mmap 段 BuildVoid0ArgSkeleton 已写完 CallInfo[depth] 5 word 字段
	//     (word0 编译期占位 0,helper 内忽略改取 calleeCI.cl word3 反查 callee
	//     Proto;funcIdx 用 caller.base + callA 算)
	//   - mmap 段 EmitFrameInlineCIDepthInc 已做 ciDepth++
	//   - thread.cur 字段未被 mmap 段更新(Go 端冷字段)
	//
	// **流程**(对应 crescent.State 实装):
	//   1. read CI[ciDepth-1].cl(BuildVoid0Arg LoadClosureGCRef 装载的 callee
	//      closure GCRef)
	//   2. 反查 callee Proto:object.ClosureProtoID(cl) → st.protos[pid]
	//   3. ciDepth-- 抵消 BuildVoid0Arg 副作用
	//   4. funcIdx = th.cur.base + callA(caller frame R(callA) = method 槽位)
	//   5. nargs=1 + nresults=0(Spike 1 SELF + CALL 0 user-arg setter 形态:
	//      SELF 已写 R(callA+1)=self,caller CALL.B=2 = 1 nargs(self only),
	//      enterLuaFrame 期望 nargs=1)
	//   6. nCcalls++/enterLuaFrame/executeFrom/popCallInfo
	//   7. 出口 ciDepth++ 平衡 PopVoid0Arg(commit-5m 入口先从 mirror sync Go
	//      field ciDepth,避免 mmap CIDepthInc 与 Go field 不同步)
	//
	// **返**:0=OK(callee 完成 + 返值已落 R(callA..callA+nresults-1))/ 1=ERR
	// (state.pendingErr 已置,Run 端 dispatcher 走错误路径)。
	//
	// **commit-5l 签名修正**(承 PR 评审 + 自检):原 retA 是 RETURN.A(setter
	// 形态恒 0),无法正确算 funcIdx;改 callA 是 CALL.A(SELF + CALL 形态下
	// method 槽位置),与 host.CallBaseline 同款语义对齐。
	//
	// **commit-5p Spike 2 签名扩**:加 callArgCount 参数,允许 N 参 SELF + CALL
	// 形态(callArgCount=0..7;helper 内 enterLuaFrame nargs = 1+callArgCount =
	// self + N user args)。
	//
	// **commit-5q Spike 4 签名扩**:加 nresults 参数,允许多返值形态(
	// callC=1 → 0返 setter / callC=2 → 1返 getter / callC=3..16 → N=2..15 返
	// drop multi-ret;helper 内 enterLuaFrame nresults 设值 + callee RETURN
	// doReturn 自动落 R(callA..callA+nresults-1))。
	ExecuteCalleeFromInlineFrame(base int32, callA int32, callArgCount int32, nresults int32) int32
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
