// Bridge — P2 主结构,贯穿计数 / IC 反馈 / 可编译性 / 状态机 / 升层日志
// (`docs/design/p2-bridge/01-profiling.md` §6 + `04-try-compile-fallback.md` §3-§5)。
package bridge

import (
	"fmt"
	"sync"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// Bridge 是 P2 决策机的主结构(每个 State 一份;profileTable 挂 State 私有,
// 跨 goroutine 不共享——01 §6.3 (B) 方案)。
//
// 注入式接线(避免反向依赖 internal/crescent):
//   - p3 由门面层(wangshu.NewState)在 P3 上线后注入;P1-only build / P2 PB0
//     不注入,p3 == nil → SupportsAllOpcodes 返 false → F7 永久判不可编译,
//     与 P1 行为一致。
//   - logger 由门面层注入 stdLogger 默认实现;测试可注入捕获式 Logger 验证
//     升层日志格式(04 §6.5 测试断言)。
type Bridge struct {
	// profileTable: Proto -> ProfileData(State 私有,01 §6.3)。
	// 惰性建表:首次 onBackEdge / onEnter 命中 Proto 时分配。
	profileTable map[*bytecode.Proto]*ProfileData

	// p3: P3/P4 编译器接口(05 §2)。注入式;nil 表示 P1-only / mock 未装载。
	p3 P3Compiler

	// gibbousCodes: 已升层 Proto → GibbousCode(04 §4.4 installGibbous 安装
	// 之后挂在此处,防止 wasm module / 原生码段被 GC 早释放,直到 Bridge
	// 自身释放为止)。
	gibbousCodes map[*bytecode.Proto]GibbousCode

	// compileMu: try-compile + installGibbous 关键段的全局互斥锁
	// (04 §4.5 (A) 方案——Bridge 级粗粒度,简单可靠;多 Proto 不并行
	// 编译是合理代价,因为 P2 不在热路径)。
	compileMu sync.Mutex

	// logger: 升层日志诊断接口(04 §6.4)。注入式;nil 时 silentLogger
	// 替代(单测默认场景)。
	logger Logger

	// aggregator: IC 反馈聚合器(02 §6.4)。Bridge 内嵌一份,considerPromotion
	// 升层时调 Aggregate(proto) 产 TypeFeedback 喂给 P3。
	aggregator *Aggregator
}

// NewBridge 构造一个空 Bridge,挂在 State 上(crescent 端 setter 注入)。
//
// p3 / logger 都可以后续 SetXxx 注入;构造时不强求(支持「先建 Bridge,后
// 注入 P3」这种 P3 落地前的过渡形态)。
func NewBridge() *Bridge {
	return &Bridge{
		profileTable: make(map[*bytecode.Proto]*ProfileData),
		gibbousCodes: make(map[*bytecode.Proto]GibbousCode),
		aggregator:   NewAggregator(),
	}
}

// SetP3Compiler 注入 P3/P4 编译器。可在 Bridge 创建后任意时刻调用,
// 但**实际编译触发**(considerPromotion 走 try-compile 路径)前必须装好。
func (b *Bridge) SetP3Compiler(p3 P3Compiler) { b.p3 = p3 }

// SetLogger 注入升层日志接口(测试可捕获;门面层装 stdLogger 默认实现)。
func (b *Bridge) SetLogger(l Logger) { b.logger = l }

// Aggregator 暴露 IC 反馈聚合器供 considerPromotion 升层路径调
// (Aggregate(proto) → *TypeFeedback)。**P2 写不消费**(02 §7):
// installFeedback 写 ProfileData.Feedback,P2 自身不读此字段。
func (b *Bridge) Aggregator() *Aggregator { return b.aggregator }

// ProfileOf 返回 Proto 在本 State 上的 ProfileData,惰性建表。
//
// **不是热路径常用接口**:onBackEdge / onEnter 内部走 profileOf 拿 pd,但
// 这是为内部调用设计的;外部诊断工具用此接口。
func (b *Bridge) ProfileOf(proto *bytecode.Proto) *ProfileData { return b.profileOf(proto) }

// profileOf 是 ProfileOf 的内部别名。命名一致性:Bridge 内私有 helper 用
// 小写,公共 API 大写。
//
// 惰性建表时从 Proto 旁字段同步 Compilability(Compile 时已经写过,跨 State
// 一致;profileTable 是 State 私有副本,首次见 Proto 时复制一次)。
func (b *Bridge) profileOf(proto *bytecode.Proto) *ProfileData {
	pd, ok := b.profileTable[proto]
	if !ok {
		pd = &ProfileData{
			Compilable: Compilability(proto.Compilability),
			Reasons:    ReasonsBitmap(proto.CompReasons),
		}
		b.profileTable[proto] = pd
	}
	return pd
}

// CompilabilityOf 04 状态机查询入口(只读,03 §5.3)。
//
// 优先读 Proto 旁字段(Compile 时一次写,跨 State 共享只读);若 Proto 字段
// 为零(P1-only 未跑 AnalyzeProto)则回退查 profileTable(支持 considerPromotion
// 路径中可能出现的 SetCompilability 直接写——主要用于测试)。
//
// 字段是编译期一次写、运行期只读(03 §5.4),无需 atomic / mutex。
func (b *Bridge) CompilabilityOf(proto *bytecode.Proto) Compilability {
	if c := Compilability(proto.Compilability); c != CompUnknown {
		return c
	}
	pd, ok := b.profileTable[proto]
	if !ok {
		return CompUnknown
	}
	return pd.Compilable
}

// SetCompilability Compile 时一次写入(由 AnalyzeProto 调用,PB3 落地)。
//
// 优先写 Proto 旁字段(跨 State 共享只读);同时写 profileTable 副本以便
// considerPromotion 内的 pd.Compilable 读取一致(本期实装让 considerPromotion
// 走 pd.Compilable 而非 CompilabilityOf,故需保持二者同步)。
//
// **不变式**(03 §5.4):字段只在 Compile 阶段一次写;运行期任何路径都不
// 修改 Compilable / Reasons。
func (b *Bridge) SetCompilability(proto *bytecode.Proto, c Compilability, r ReasonsBitmap) {
	proto.Compilability = uint8(c)
	proto.CompReasons = uint16(r)
	pd := b.profileOf(proto)
	pd.Compilable = c
	pd.Reasons = r
}

// OnBackEdge 回边采样钩点(01 §4.1)。
//
// 调用契约(由 crescent 主循环 FORLOOP / JMP 回跳后调用,PB1 接线):
//   - proto:当前帧 Proto;
//   - pc:回边目标 pc(已 += SBx 后的值)。
//
// **保证零分配**(常态未越阈值):map 查找 + 切片索引 + 自增 + 比较——一次
// 函数调用约 24ns 预算(01 §4.5 估算)。
func (b *Bridge) OnBackEdge(proto *bytecode.Proto, pc int32) {
	pd := b.profileOf(proto)
	if pd.TierState != TierInterp {
		return // 已升 Gibbous / 已卡 Stuck:无需再计数(01 §4.1 守卫)
	}
	pd.allocBackEdge(proto)
	if pc < 0 || int(pc) >= len(pd.BackEdge) {
		return // 防御性边界检查(理论上不该发生)
	}
	pd.BackEdge[pc]++
	if pd.BackEdge[pc] >= HotBackEdgeThreshold {
		b.considerPromotion(proto, pd)
	}
}

// OnEnter 函数入口采样钩点(01 §4.2)。
//
// 调用契约(由 crescent enterLuaFrame / TAILCALL 重载后调用,PB1 接线)。
func (b *Bridge) OnEnter(proto *bytecode.Proto) {
	pd := b.profileOf(proto)
	if pd.TierState != TierInterp {
		return
	}
	pd.EntryCount++
	if pd.EntryCount >= HotEntryThreshold {
		b.considerPromotion(proto, pd)
	}
}

// considerPromotion 升层决策入口(04 §3)。
//
// 调用契约(参考 [04 §3.1] + [01 §4.3]):
//  1. 幂等:多次调用不出错——本函数自身用 pd.TierState != TierInterp 守卫;
//  2. 不重载 frame——onBackEdge/onEnter 调用方无需 reloadFrame;
//  3. 不在最热路径——只在阈值临界点发生,摊薄到每回边几十 ns;
//  4. 无返回值——升/留/失败都通过 pd.TierState 表达。
//
// 处理路径(四条,对应 04 §3.2):
//
//	(P1) 已经在吸收态(TierGibbous / TierStuck) → 直接 return(防抖)
//	(P2) Compilable != CompCompilable(含 CompUnknown / CompNotCompilable)→ 转 Stuck
//	(P3) 可编译 → try-compile;成功转 Gibbous 安装;失败转 Stuck。
func (b *Bridge) considerPromotion(proto *bytecode.Proto, pd *ProfileData) {
	// (P1) 已转吸收态 → no-op(双重防抖,onBackEdge/OnEnter 守卫已先拦一道)
	if pd.TierState != TierInterp {
		return
	}

	comp := pd.Compilable
	if comp != CompCompilable {
		// (P2) 不可编译 / 未分析 → 永久解释(04 §1.4 静态 fallback)
		pd.TierState = TierStuck
		pd.CompileTried = true
		if b.logger != nil {
			b.logger.LogStuck(proto, pd, comp)
		}
		return
	}

	// (P3) try-compile
	// 加锁:多 State 共享 Proto 时只让一个 State 真正编译(04 §4.5 (A) 方案)。
	// profileTable 是 State 私有,但 gibbousCodes 是 Bridge 共享——锁守住后者
	// 与 trampoline 注册的关键段。
	b.compileMu.Lock()
	defer b.compileMu.Unlock()

	// 加锁后双重检查 gibbousCodes:别的 State 抢先编译并安装了 → 复用现有
	// GibbousCode,不重复编译(04 §4.5)。
	if existing, ok := b.gibbousCodes[proto]; ok {
		_ = existing
		pd.TierState = TierGibbous
		pd.CompileTried = true
		if b.logger != nil {
			b.logger.LogPromoted(proto, pd)
		}
		return
	}

	// 聚合 IC feedback 喂给 P3(02 §4.5 一次性聚合)
	fb := b.aggregator.Aggregate(proto)
	pd.Feedback = fb
	pd.CompileTried = true

	code, err := b.tryCompile(proto, fb)
	if err != nil {
		// (P3-fail)编译失败 → 永久解释(04 §1.4 try-compile fallback)
		pd.TierState = TierStuck
		if b.logger != nil {
			b.logger.LogCompileFail(proto, pd, err)
		}
		return
	}

	// (P3-success)升层成功:登记 gibbous 代码 + 转 TierGibbous
	b.installGibbous(proto, code)
	pd.TierState = TierGibbous
	if b.logger != nil {
		b.logger.LogPromoted(proto, pd)
	}
}

// tryCompile 包装 P3.Compile + defer recover 兜底(04 §5.2)。
//
// **后端 panic 不穿越本接口**——P2 不能因后端 bug 让 P1 主循环崩溃,
// 只能 fallback 该 Proto。recover 把 panic 转成 *CompileError(Kind=Panic),
// considerPromotion 见到 err != nil 走 fallback 路径转 Stuck。
func (b *Bridge) tryCompile(proto *bytecode.Proto, fb *TypeFeedback) (code GibbousCode, err error) {
	if b.p3 == nil {
		// 没注入 P3 不应走到这里(F7 应在 AnalyzeProto 阶段拦),防御性兜底。
		return nil, &CompileError{
			Kind:   CompileErrBackendUnsupported(),
			Proto:  proto,
			Reason: "P3 compiler not injected",
		}
	}
	defer func() {
		if r := recover(); r != nil {
			err = &CompileError{
				Kind:   CompileErrBackendPanic,
				Proto:  proto,
				Reason: fmtPanic(r),
			}
			code = nil
			if b.logger != nil {
				b.logger.LogPanic(proto, r)
			}
		}
	}()
	return b.p3.Compile(proto, fb)
}

// installGibbous 升层成功后安装 gibbous 代码(04 §4.4)。
//
// 当前简化版:只挂 gibbousCodes map(GC 防早释)。**P3 trampoline 注册**与
// **CallInfo bit50 写入**都在 PB6/P3 落地时补上——本期 P3 还没真存在,
// installGibbous 没有「下一帧改流向」的实际效果(P1 主循环不读 callInfo.gibbous,
// 见 callInfo 字段注释)。
func (b *Bridge) installGibbous(proto *bytecode.Proto, code GibbousCode) {
	b.gibbousCodes[proto] = code
}

// fmtPanic 把 recover 拿到的 panic 值格式化(避免直接对 interface{} 用 %v
// 错过 stack 信息)。简化版——P2 PB5 落地完整 stack 时升级。
func fmtPanic(r interface{}) string {
	return fmt.Sprintf("%v", r)
}

// CompileErrBackendUnsupported 是「P3 未注入」的内部错误码(04 §5.2 边角)。
// 不暴露在 CompileErrKind 枚举常量里——用一个 helper 函数避免 enum 增加
// 不必要的对外语义(P3 注入是 Bridge 装配阶段的事,不是运行期常态)。
func CompileErrBackendUnsupported() CompileErrKind { return CompileErrBackendDeclined }
