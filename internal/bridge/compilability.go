// Compilability enum and reasons bitmap (`docs/design/p2-bridge/03-compilability-analysis.md`).
package bridge

// Compilability 描述一个 Proto 的静态可编译性判定结果(03 §5.1)。
//
// 三态枚举:
//   - CompUnknown      未分析(P1-only build / Compile 还没跑 AnalyzeProto)
//   - CompCompilable   F1-F7 全部通过,可参与升层决策
//   - CompNotCompilable F1-F7 任一触发,永久解释
//
// **重要纪律**(03 §1):**保守第一,宁漏勿误**——把不可编译形状判成可编译
// 的后果是灾难性的(P3 编译错误代码或运行期崩溃,fallback 不会被触发,
// 系统不知道结果是错的);把可编译的判成不可编译只是「少赚加速」。所以
// 任何拿不准的形状一律判 CompNotCompilable。
type Compilability uint8

const (
	// CompUnknown:Compile 尚未跑 AnalyzeProto(或 P1-only build 未启 P2)。
	// 04 状态机视同 NotCompilable(再保守一层,03 §5.5),保守不升层。
	CompUnknown Compilability = iota

	// CompCompilable:可编译——F1-F7 全部通过,可参与升层决策。
	// 升层后 04 状态机可调 P3 编译。
	CompCompilable

	// CompNotCompilable:不可编译——F1-F7 任一触发,永久解释。
	// 04 状态机的 considerPromotion 直接跳过此 Proto(永远 tier-0)。
	CompNotCompilable
)

func (c Compilability) String() string {
	switch c {
	case CompCompilable:
		return "Compilable"
	case CompNotCompilable:
		return "NotCompilable"
	default:
		return "Unknown"
	}
}

// ReasonsBitmap 是 F1-F7 拒因的位掩码(03 §5.1 reasonsBitmap)。
//
// 每个 F<n> 形状对应一位(下文常量按设计文档 03 §3 顺序排列);Compilable
// 时为 0;NotCompilable 时至少一位为 1(可能多位同时——「保守第一」的体现:
// 多条规则同时判不可编译,冗余 = 安全)。
type ReasonsBitmap uint16

const (
	// ReasonVararg(F1):vararg 函数(03 §3.1)。
	ReasonVararg ReasonsBitmap = 1 << iota

	// ReasonYield(F2-a):直接调 coroutine.yield。
	ReasonYield

	// ReasonResume(F2-a'):直接调 coroutine.resume。
	ReasonResume

	// ReasonCoroutine(F2-a''):任何 coroutine.* 调用。
	ReasonCoroutine

	// ReasonUnknownCall(F2-b):调用了无法静态确定不 yield 的函数。
	ReasonUnknownCall

	// ReasonDebug(F3):引用了 debug 表(03 §3.3)。
	ReasonDebug

	// ReasonSetfenv(F4):调用了 setfenv / getfenv(03 §3.4)。
	ReasonSetfenv

	// ReasonOverSize(F5):函数指令数超 MaxCompilableInsns(03 §3.5)。
	ReasonOverSize

	// ReasonOverRegs(F5):寄存器数超 MaxCompilableRegs(03 §3.5)。
	ReasonOverRegs

	// ReasonNestedDeep(F6):嵌套深度超 MaxClosureDepth(03 §3.6)。
	ReasonNestedDeep

	// ReasonOverUpval(F6):upvalue 数超 MaxUpvalCount(03 §3.6)。
	ReasonOverUpval

	// ReasonBackendUnsupp(F7):P3 后端不支持某 opcode(03 §3.7)。
	ReasonBackendUnsupp
)

// HasAny reports whether any reason bit is set.
func (r ReasonsBitmap) HasAny() bool { return r != 0 }

// 阈值常量(03 §3.5 / §3.6 建议值——实测后定标,见 03 §9 缺口)。
const (
	// MaxCompilableInsns:Proto.Code 长度上限,超出判 F5。
	// 2000 指令 ≈ 100-300 行 Lua,绝大多数热点函数远小于此。
	MaxCompilableInsns = 2000

	// MaxCompilableRegs:Proto.MaxStack 上限,超出判 F5。
	// 200 是 Lua 5.1 单函数寄存器合理上限(02 §1 max regs ≈ 250)的余量版本。
	MaxCompilableRegs = 200

	// MaxClosureDepth:嵌套函数深度上限(F6)。
	// 偏严保守(嵌套 1-2 层是绝大多数);P3 upvalue 编译协议成熟后放宽。
	MaxClosureDepth = 3

	// MaxUpvalCount:Proto.UpvalDescs 长度上限(F6)。
	MaxUpvalCount = 8
)
