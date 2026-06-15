// P3Compiler interface (`docs/design/p2-bridge/05-p3-p4-interface.md` §2).
//
// 「共享前端」核心契约——P3(wazero)/ P4(原生)/ mock 三方共用同一份
// 接口面;P2 实现端零修改。
package bridge

import "github.com/Liam0205/wangshu/internal/bytecode"

// P3Compiler 是 P2 调下游编译层的核心接口(05 §2.1)。
//
// **接口稳定性是硬约束**(05 §0.3)——P2 实现端定稳之后,P3 上线 / P4 上线 /
// mock 切换都按同一份接口对接,Bridge.considerPromotion / installGibbous 代码
// 完全不动。任何「让 P3 / P4 各自重新设计热度采样 / 类型反馈 / 升层判断」
// 的实现都直接判否。
type P3Compiler interface {
	// SupportsAllOpcodes 检查 Proto 中所有 opcode 是否都在后端支持集内。
	//
	// 调用方:[03] §3.7 F7 闸门(AnalyzeProto 中调用,作为 F1-F6 全过后
	// 的 opcode 级兜底)。
	//
	// 实现方契约:
	//   - O(N) 单遍扫 proto.Code;任一未支持即返 false;
	//   - 调用纯只读;不修改 Proto、不持久化任何状态;
	//   - 见到未识别 opcode 编号(38..63 预留区间或未来扩展)统一返 false
	//     (保守拒,03 §3.7.4 「保守缺省」原则);
	//   - **不应 panic**——遇到无法识别的 opcode 编号也走保守拒。
	SupportsAllOpcodes(proto *bytecode.Proto) bool

	// Compile 把 Proto 编译成 GibbousCode(可执行产物)。
	//
	// 调用方:[04] §3 considerPromotion 在确认
	//   ① TierState == TierInterp ② Compilable == CompCompilable
	//   ③ 热度越阈值 后调用本函数。
	//
	// 入参:
	//   - proto:目标 Proto(已通过 F1-F7 闸门,可编译);
	//   - feedback:类型反馈快照(02 §4 聚合产物);**实现方必须容忍 nil**
	//     (退化为「无 feedback 提示」编译,仍正确)。
	//
	// 错误返回语义(关键契约,04 §4.3 + 05 §2.2.2):
	//   - error != nil ⇒ P2 把该 Proto 标 TierStuck(永久解释,不重试);
	//     本调用是「该 Proto 升层尝试」的唯一一次,失败即 fallback;
	//   - 实现方应区分错误类型(unsupported_opcode_shape /
	//     out_of_resources / backend_panic / backend_declined);
	//   - 后端 panic ⇒ 实现方 recover 转 error,**不让 panic 穿越本接口**
	//     (P2 不能因后端 bug 崩溃,只能 fallback 该 Proto);
	//
	// 性能要求:Compile 不在热路径(只在升层时一次性调用),数毫秒级可接受。
	//
	// 并发要求:可被多 State 并发调用同一 Proto;实现方需保证线程安全。
	Compile(proto *bytecode.Proto, feedback *TypeFeedback) (GibbousCode, error)
}

// GibbousCode 是 P3/P4 编译产物的「安装句柄」(05 §6 抽象类型)。
//
// P2 视角:不透明 token——installGibbous(proto, code) 把它登记到 P3
// trampoline 表;P2 不解读其内部字段。具体类型由 P3(wazero CompiledModule)
// 或 P4(原生码段)实现。
//
// **Run/PendingErr 跨层执行入口(VS0-d)**:crescent 的 trampoline 在 doCall
// 检测到 Proto 已升 gibbous 时,经此接口跳进编译产物执行(05 §6.2)。把 Run
// 放接口上(而非让 crescent 类型断言到 gibbous 私有类型)使 trampoline 逻辑
// 留在 crescent 全 build 代码,不 import p3-build-only 的 gibbous 包——P3/P4
// 共用同一套 trampoline(04-trampoline §0.4)。
type GibbousCode interface {
	// Proto 反向指针,trampoline 校验用——确保 GibbousCode 与 Proto 配对。
	Proto() *bytecode.Proto

	// Run 是 crescent→gibbous 跨层入口(04-trampoline §2.2 step3)。
	//   - stack:复用栈(CallWithStack 零分配路径,len≥1);stack[0]=base 入参,
	//     返回后 stack[0]=status。
	//   - base:本帧 R0 在共见 linear memory 的字节偏移(= stackSegByte+base*8)。
	//   - 返回 status:0=OK / 1=ERR(05 §2.1)。P3 永不返回 2(deopt 是 P4 专用)。
	Run(stack []uint64, base uint32) int32

	// PendingErr 返回最近一次 Run 的 wazero 内部错误(trampoline ERR 时读)。
	PendingErr() error

	// Slot 返回本编译产物 run 在共享 env.table 的槽号 + 是否已登记(P3 PW10 R3
	// Arch-2)。gibbous→gibbous CALL 据被调 GibbousCode 的 slot 经 call_indirect
	// 跨 module 直达(免 h_call 双跨层)。ok=false(表满哨兵 / 未入表)⟹ 回退
	// 同步 Run(baseline)。P4 原生码无 wasm 表概念,返 (0,false) 即可(永走回退)。
	Slot() (uint32, bool)
}

// CompileErrKind 编译失败的类别(05 §2.2.2 错误返回语义 / 04 §4.3)。
//
// 让升层日志能区分「F7 漏判」(03 的 bug)/ 「资源耗尽」(运行期临时态)/
// 「后端 panic」(P3 的 bug),诊断工具据此分流告警。
type CompileErrKind uint8

const (
	// CompileErrUnsupportedOpcodeShape:F7 漏判——SupportsAllOpcodes 没识别
	// 出某 opcode 的某子情况(如 GETTABLE 的 key 是某种特殊形态);[03] 整体
	// 判 Compilable 但 P3 实际编不了。应记 issue 修 [03]。
	CompileErrUnsupportedOpcodeShape CompileErrKind = iota

	// CompileErrOutOfResources:wazero module 实例化失败 / 内存不足 / 资源
	// 上限。理论可重试,但 P2 不区分(04 §7.1 不重试纪律)。
	CompileErrOutOfResources

	// CompileErrBackendPanic:P3 编译器内部 panic(实现 bug 或边角形态)。
	// 由 b.tryCompile 的 defer recover 兜底转此类别。应记 issue 修 P3。
	CompileErrBackendPanic

	// CompileErrBackendDeclined:P3 决定不编译(如启发式判该 Proto 收益不够)。
	// P2 PB0 不预期此类返回(P3 应在 SupportsAllOpcodes 阶段拒)。
	CompileErrBackendDeclined
)

func (k CompileErrKind) String() string {
	switch k {
	case CompileErrUnsupportedOpcodeShape:
		return "unsupported_opcode_shape"
	case CompileErrOutOfResources:
		return "out_of_resources"
	case CompileErrBackendPanic:
		return "backend_panic"
	case CompileErrBackendDeclined:
		return "backend_declined"
	default:
		return "unknown"
	}
}

// CompileError 是 P3.Compile 返回 err 的标准包装(04 §5.2)。
//
// b.tryCompile 的 defer recover 把后端 panic 转成 *CompileError(Kind=
// CompileErrBackendPanic)——避免 panic 穿越接口让 P1 解释器主循环崩溃。
type CompileError struct {
	Kind   CompileErrKind
	Proto  *bytecode.Proto
	Reason string // 可读原因(包含 panic stack / OOM 描述等)
}

func (e *CompileError) Error() string { return e.Kind.String() + ": " + e.Reason }
