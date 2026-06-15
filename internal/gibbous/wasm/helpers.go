//go:build wangshu_p3

package wasm

// imported helper 的 Go 侧实装(02-translation §6.4 + 04-trampoline §3)。
//
// **依赖解环**:helper 需要操作执行期状态(CallInfo、值栈、upvalue、返回值
// 回填),这些归 crescent.State。但 crescent 要 import gibbous/wasm 取
// p3Code.Run(trampoline),若 gibbous/wasm 反 import crescent 则成环。
// 解法(同 P2 bridge 不依赖 crescent 的接口注入手法):gibbous/wasm 定义
// helper 需要的最小抽象接口 HostState,crescent 实现并在升层安装时注入。
//
// PW2 helper 集:h_getupval / h_setupval / h_return / h_safepoint。

import (
	"context"

	"github.com/tetratelabs/wazero/api"
)

// HostState 是 gibbous helper 操作执行期状态的最小抽象(crescent.State 实现)。
//
// 所有方法以 base(当前帧 R0 的字节偏移)为坐标 —— gibbous 帧与解释器帧
// 共享同一值栈(03-memory-model),base 唯一定位本帧。
//
// 方法语义直接复用解释器侧已有实装(execute.go 对应行),保证 byte-equal。
type HostState interface {
	// GetUpval 取当前 closure 的 upvalue B 的值(execute.go GETUPVAL 段)。
	GetUpval(base int32, b int32) uint64
	// SetUpval 写当前 closure 的 upvalue B(execute.go SETUPVAL 段)。
	SetUpval(base int32, b int32, val uint64)
	// DoReturn 处理 RETURN A B:返回值回填到调用者期望槽 + 记 savedPC。
	// 返回 status(0=OK / 1=ERR);gibbous 函数据此 return。
	DoReturn(base int32, pc int32, a int32, b int32) int32
	// Safepoint 回边 GC 检查点(PW4 用;PW2 先声明)。
	Safepoint(base int32, pc int32)
	// SetSavedPC 写回 CallInfo.savedPC(pc 物化,02 §4.2)。
	SetSavedPC(base int32, pc int32)

	// --- PW3 算术慢路径助手(快路径双 number 在 Wasm 内直发 f64,失败回 Go)---

	// Arith 处理算术 opcode 慢路径(ADD/SUB/MUL/DIV/MOD/POW)的 coercion +
	// 元方法链(复用 execute.go doArithSlow)。op 是 bytecode.OpCode 值。
	// 返回 status(0=OK / 1=ERR)。
	Arith(base, pc, op, b, c, a int32) int32
	// Unm 处理 UNM 慢路径(string coercion + __unm)。
	Unm(base, pc, b, a int32) int32
	// Len 处理 LEN(string 长度 / table border / 异类报错;复用 execute.go LEN 段)。
	Len(base, pc, b, a int32) int32
	// Concat 处理 CONCAT(复用 execute.go doConcat 全逻辑 + safepoint)。
	Concat(base, pc, a, b, c int32) int32

	// --- PW4 控制流慢路径助手 ---

	// Compare 处理 LT/LE 慢路径(string 比较 / __lt/__le 元方法)。op 是
	// bytecode.OpCode。返回 packed:bit0=比较结果(0/1),bit1=错误标志。
	Compare(base, pc, op, b, c int32) int32
	// Eq 处理 EQ 的 __eq 元方法路径(raw 不等时;复用 rawEqual + __eq)。
	// 返回 packed:bit0=结果,bit1=错误。
	Eq(base, pc, b, c int32) int32
	// ForPrep 处理 FORPREP:三槽校验 + coercion + 预减(复用 execute.go
	// FORPREP 段,byte-equal 错误消息)。返回 status(0=OK / 1=ERR)。
	ForPrep(base, pc, a int32) int32

	// --- PW5 表 IC 慢路径助手(快路径 inline 跳哈希,失效/复杂形态回 Go)---

	// GetTable 处理 GETTABLE A B C 慢路径(icGetTable 完整查找 + __index)。
	// 返回 status(0=OK / 1=ERR)。
	GetTable(base, pc, a, b, c int32) int32
	// SetTable 处理 SETTABLE A B C 慢路径(icSetTable 完整写 + __newindex + safepoint)。
	SetTable(base, pc, a, b, c int32) int32
	// GetGlobal 处理 GETGLOBAL A Bx 慢路径(globals 表 icGetTable)。
	// 名带 Do 前缀避开 State 公有 API GetGlobal(name string) 冲突。
	DoGetGlobal(base, pc, a, bx int32) int32
	// SetGlobal 处理 SETGLOBAL A Bx 慢路径(globals 表 icSetTable + safepoint)。
	DoSetGlobal(base, pc, a, bx int32) int32
	// Self 处理 SELF A B C(R(A+1):=R(B) + icGetTable;助手内含 self 传递,幂等)。
	Self(base, pc, a, b, c int32) int32
	// NewTable 处理 NEWTABLE A B C(allocTable + setReg + safepoint,全助手内分配)。
	NewTable(base, pc, a, b, c int32) int32
	// SetList 处理 SETLIST A B C(doSetList 批量写 + 可能 rehash + safepoint)。
	SetList(base, pc, a, b, c int32) int32

	// Call 处理 CALL A B C 三向分派(crescent/gibbous/host,04-trampoline §3)。
	// 跑被调帧到完成,返回值留 R(A..) 共见栈槽。
	// 返回:成功 = **刷新后的本帧 base 字节偏移**(嵌套调用可能 growStack 段重定位,
	// gibbous 须用此新 base 续算寻址,否则陈旧 $base 指向已 Free 旧段 = UAF);
	// 错误 = 负哨兵 -1(pendingErr 已置,status 链冒泡)。
	DoCall(base, pc, a, b, c int32) int64

	// TailCall 处理 TAILCALL A B C(尾调用复用帧,04-trampoline §2.5)。
	// 复用 doTailCall 关 upvalue + 下移参数 + 改写当前 CallInfo,然后同步驱动
	// 复用帧到完成(executeFrom,保持 proper tail call O(1) 栈)。返回值已落
	// 调用者期望槽。返回 status(0=OK gibbous 函数应直接 return 0 / 1=ERR)。
	TailCall(base, pc, a, b, c int32) int32

	// Closure 处理 CLOSURE A Bx(makeClosure + setReg + safepoint,分配在助手内)。
	// 后随伪指令由 makeClosure 读 ci.pc 消化(发射侧已跳过翻译)。返回 status(0/1)。
	Closure(base, pc, a, bx int32) int32
	// Close 处理 CLOSE A(关闭 ≥ base+A 的开放 upvalue,纯状态操作)。返回 status(恒 0)。
	Close(base, pc, a int32) int32
	// TForLoop 处理 TFORLOOP A C(调迭代器 R(A)(R(A+1),R(A+2)),结果落 R(A+3..))。
	// 迭代器调用经 callLuaFromHost 可能 growStack 段重定位 → 返回刷新后的本帧 base:
	//   ≥0 = 新 base 字节偏移(首值非 nil,继续循环);-1 = ERR;-2 = 退出(首值 nil)。
	TForLoop(base, pc, a, c int32) int64

	// GlobalsRaw 返回 globals 表的 NaN-box u64(编译期烧立即数,GETGLOBAL/SETGLOBAL
	// inline 用)。globals 在 State 生命期内身份恒定不移动。名带 Raw 避开 State
	// 公有 API Globals() arena.GCRef 冲突。
	GlobalsRaw() uint64

	// GCPendingAddr 返回 gcPending 标志字在 linear memory 的字节地址(arena GCRef,
	// P3 PW9)。gibbous FORLOOP 回边 inline `i32.load(GCPendingAddr)`——非 0 才跨层调
	// h_safepoint(否则热循环每迭代无条件跨层吞掉收益,05 §3)。State 生命期内恒定。
	GCPendingAddr() uint32

	// CITransferAddr 返回 ci-transfer 中转字在 linear memory 的字节地址(arena GCRef,
	// P3 PW10 R3)。gibbous→gibbous call_indirect 直调经此字传被调帧 base(DoCall 写,
	// caller wasm 读作 call_indirect 实参)与刷新后 caller base(DoReturn 写,call_indirect
	// 返回后 caller 读续算)。State 生命期内恒定。
	CITransferAddr() uint32

	// CIDepthAddr 返回 ci-depth 游标字在 linear memory 的字节地址(arena GCRef,
	// P3 PW10 零跨界 Stage 1a)。Wasm 侧帧建拆(Stage 2/3)increment/decrement 此 i32
	// 字免回 Go 改 th.ciDepth。State 生命期内恒定。
	CIDepthAddr() uint32

	// CISegBaseAddr 返回 ci-seg-base 字在 linear memory 的字节地址(arena GCRef,
	// P3 PW10 零跨界 Stage 2)。此字内含 CI 段当前字节基址(growCISeg 可重定位);
	// Wasm 侧帧建拆读它现算帧地址。**字地址恒定**,字内容(段基址)随段重定位变。
	CISegBaseAddr() uint32

	// OpenGuardAddr 返回 open-upvalue 守卫字在 linear memory 的字节地址(arena GCRef,
	// P3 PW10 零跨界 Stage 2)。字值 = maxOpenIdx+1(有开放 upvalue)/ 0(无);Wasm
	// RETURN 快路径守卫 frameBase ≥ 此值 ⟺ 本帧无须关闭的开放 upvalue。
	OpenGuardAddr() uint32

	// TopAddr 返回 top 镜像字在 linear memory 的字节地址(arena GCRef,P3 PW10 零跨界
	// ①)。字值 = th.top(槽索引);Wasm 建帧设 callee 帧顶 / caller 自恢复 top 时写它,
	// Go 侧 GC 栈根扫描读它定 [0,top) 上界。槽索引坐标(grow 安全)。State 生命期内恒定。
	TopAddr() uint32

	// PopErrFrame 在 call_indirect 直调失败时补弹遗留的 gibbous 被调帧(PW10 R3)。
	// 被调出错自身 return 1 不弹帧,caller wasm 据 status≠0 调本助手补弹——精确复刻
	// baseline enterGibbous ERR 路径的弹帧条件(currentCI 是 gibbous 帧才弹)。
	PopErrFrame()
}

// helperSet 持有注入的 HostState,提供给 wazero 注册的 Go callback。
//
// 每个 State 一份(arena/Runtime 单 State 私有)。wazero host module 的
// callback 闭包捕获 helperSet,调用时转发到 HostState。
type helperSet struct {
	host HostState
}

// --- wazero host function callback(零分配 stack-based,PW10 R3.5)---
//
// 用 wazero `api.GoFunc`(WithGoFunction 注册)而非 `WithFunc`(反射):反射路径
// 每次跨层 callGoFunc 都 make([]reflect.Value) + 逐参 reflect.New 装箱(实测调用
// 密集核 ~14 allocs/调,支配 call 核退化);stack-based 路径从 []uint64 直接解参/
// 回写结果,零反射零分配。
//
// 约定:stack[i] 是第 i 个入参的原始 u64(i32 经 api.DecodeI32 取低 32 位;i64 裸取);
// 结果写回 stack[0](i32 经 api.EncodeI32;i64 裸写)。参数顺序 = module.go import
// 声明的 type 形参顺序。

// goGetUpval: (base i32, b i32) -> (i64)  type 0 / h_getupval。
func (h *helperSet) goGetUpval(_ context.Context, stack []uint64) {
	base := api.DecodeI32(stack[0])
	b := api.DecodeI32(stack[1])
	stack[0] = h.host.GetUpval(base, b)
}

// goSetUpval: (base i32, b i32, val i64) -> ()  type 1 / h_setupval。
func (h *helperSet) goSetUpval(_ context.Context, stack []uint64) {
	h.host.SetUpval(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), stack[2])
}

// goReturn: (base i32, pc i32, a i32, b i32) -> (i32)  type 2 / h_return。
func (h *helperSet) goReturn(_ context.Context, stack []uint64) {
	st := h.host.DoReturn(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]))
	stack[0] = api.EncodeI32(st)
}

// goSafepoint: (base i32, pc i32) -> ()  type 3 / h_safepoint。
func (h *helperSet) goSafepoint(_ context.Context, stack []uint64) {
	h.host.Safepoint(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]))
}

// goArith: (base,pc,op,b,c,a i32) -> (i32)  type 5 / h_arith。
func (h *helperSet) goArith(_ context.Context, stack []uint64) {
	st := h.host.Arith(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]),
		api.DecodeI32(stack[3]), api.DecodeI32(stack[4]), api.DecodeI32(stack[5]))
	stack[0] = api.EncodeI32(st)
}

// goUnm: (base,pc,b,a i32) -> (i32)  type 6 / h_unm。
func (h *helperSet) goUnm(_ context.Context, stack []uint64) {
	st := h.host.Unm(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]))
	stack[0] = api.EncodeI32(st)
}

// goLen: (base,pc,b,a i32) -> (i32)  type 7 / h_len。
func (h *helperSet) goLen(_ context.Context, stack []uint64) {
	st := h.host.Len(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]))
	stack[0] = api.EncodeI32(st)
}

// goConcat: (base,pc,a,b,c i32) -> (i32)  type 8 / h_concat。
func (h *helperSet) goConcat(_ context.Context, stack []uint64) {
	st := h.host.Concat(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]), api.DecodeI32(stack[4]))
	stack[0] = api.EncodeI32(st)
}

// goCompare: (base,pc,op,b,c i32) -> (i32 packed)  type 8 / h_compare。
func (h *helperSet) goCompare(_ context.Context, stack []uint64) {
	st := h.host.Compare(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]), api.DecodeI32(stack[4]))
	stack[0] = api.EncodeI32(st)
}

// goEq: (base,pc,b,c i32) -> (i32 packed)  type 6 / h_eq。
func (h *helperSet) goEq(_ context.Context, stack []uint64) {
	st := h.host.Eq(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]))
	stack[0] = api.EncodeI32(st)
}

// goForPrep: (base,pc,a i32) -> (i32 status)  type 9 / h_forprep。
func (h *helperSet) goForPrep(_ context.Context, stack []uint64) {
	st := h.host.ForPrep(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]))
	stack[0] = api.EncodeI32(st)
}

// --- PW5 表 IC 助手 callback ---

func (h *helperSet) goGetTable(_ context.Context, stack []uint64) {
	st := h.host.GetTable(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]), api.DecodeI32(stack[4]))
	stack[0] = api.EncodeI32(st)
}

func (h *helperSet) goSetTable(_ context.Context, stack []uint64) {
	st := h.host.SetTable(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]), api.DecodeI32(stack[4]))
	stack[0] = api.EncodeI32(st)
}

func (h *helperSet) goGetGlobal(_ context.Context, stack []uint64) {
	st := h.host.DoGetGlobal(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]))
	stack[0] = api.EncodeI32(st)
}

func (h *helperSet) goSetGlobal(_ context.Context, stack []uint64) {
	st := h.host.DoSetGlobal(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]))
	stack[0] = api.EncodeI32(st)
}

func (h *helperSet) goSelf(_ context.Context, stack []uint64) {
	st := h.host.Self(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]), api.DecodeI32(stack[4]))
	stack[0] = api.EncodeI32(st)
}

func (h *helperSet) goNewTable(_ context.Context, stack []uint64) {
	st := h.host.NewTable(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]), api.DecodeI32(stack[4]))
	stack[0] = api.EncodeI32(st)
}

func (h *helperSet) goSetList(_ context.Context, stack []uint64) {
	st := h.host.SetList(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]), api.DecodeI32(stack[4]))
	stack[0] = api.EncodeI32(st)
}

// goCall: (base,pc,a,b,c i32) -> (i64)  type 10 / h_call(返回 3 态哨兵,裸 i64)。
func (h *helperSet) goCall(_ context.Context, stack []uint64) {
	ret := h.host.DoCall(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]), api.DecodeI32(stack[4]))
	stack[0] = uint64(ret)
}

func (h *helperSet) goTailCall(_ context.Context, stack []uint64) {
	st := h.host.TailCall(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]), api.DecodeI32(stack[4]))
	stack[0] = api.EncodeI32(st)
}

func (h *helperSet) goClosure(_ context.Context, stack []uint64) {
	st := h.host.Closure(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]))
	stack[0] = api.EncodeI32(st)
}

func (h *helperSet) goClose(_ context.Context, stack []uint64) {
	st := h.host.Close(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]))
	stack[0] = api.EncodeI32(st)
}

// goTForLoop: (base,pc,a,c i32) -> (i64)  type 11 / h_tforloop(裸 i64 哨兵)。
func (h *helperSet) goTForLoop(_ context.Context, stack []uint64) {
	ret := h.host.TForLoop(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]))
	stack[0] = uint64(ret)
}

// goCallErr: () -> ()  type 12 / h_callerr(PW10 R3:补弹遗留 gibbous 帧)。
func (h *helperSet) goCallErr(_ context.Context, _ []uint64) {
	h.host.PopErrFrame()
}
