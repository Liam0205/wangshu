// Bridge — P2 主结构,贯穿计数 / IC 反馈 / 可编译性 / 状态机 / 升层日志
// (`docs/design/p2-bridge/01-profiling.md` §6 + `04-try-compile-fallback.md` §3-§5)。
package bridge

import (
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
}

// NewBridge 构造一个空 Bridge,挂在 State 上(crescent 端 setter 注入)。
//
// p3 / logger 都可以后续 SetXxx 注入;构造时不强求(支持「先建 Bridge,后
// 注入 P3」这种 P3 落地前的过渡形态)。
func NewBridge() *Bridge {
	return &Bridge{
		profileTable: make(map[*bytecode.Proto]*ProfileData),
		gibbousCodes: make(map[*bytecode.Proto]GibbousCode),
	}
}

// SetP3Compiler 注入 P3/P4 编译器。可在 Bridge 创建后任意时刻调用,
// 但**实际编译触发**(considerPromotion 走 try-compile 路径)前必须装好。
func (b *Bridge) SetP3Compiler(p3 P3Compiler) { b.p3 = p3 }

// SetLogger 注入升层日志接口(测试可捕获;门面层装 stdLogger 默认实现)。
func (b *Bridge) SetLogger(l Logger) { b.logger = l }

// ProfileOf 返回 Proto 在本 State 上的 ProfileData,惰性建表。
//
// **不是热路径常用接口**:onBackEdge / onEnter 内部走 profileOf 拿 pd,但
// 这是为内部调用设计的;外部诊断工具用此接口。
func (b *Bridge) ProfileOf(proto *bytecode.Proto) *ProfileData { return b.profileOf(proto) }

// profileOf 是 ProfileOf 的内部别名。命名一致性:Bridge 内私有 helper 用
// 小写,公共 API 大写。
func (b *Bridge) profileOf(proto *bytecode.Proto) *ProfileData {
	pd, ok := b.profileTable[proto]
	if !ok {
		pd = &ProfileData{Compilable: CompUnknown} // 03 §5.5 占位
		b.profileTable[proto] = pd
	}
	return pd
}

// CompilabilityOf 04 状态机查询入口(只读,03 §5.3)。
//
// 字段是编译期一次写、运行期只读(03 §5.4),无需 atomic / mutex。
func (b *Bridge) CompilabilityOf(proto *bytecode.Proto) Compilability {
	pd, ok := b.profileTable[proto]
	if !ok {
		return CompUnknown
	}
	return pd.Compilable
}

// SetCompilability Compile 时一次写入(由 AnalyzeProto 调用,PB3 落地)。
//
// **不变式**(03 §5.4):字段只在 Compile 阶段一次写;运行期任何路径都不
// 修改 Compilable / Reasons。
func (b *Bridge) SetCompilability(proto *bytecode.Proto, c Compilability, r ReasonsBitmap) {
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
// 当前 PB0 实装是**no-op 占位**——状态机本体在 PB4 落地。
// 占位的语义保留:任何越阈值都不真正升层(留 TierInterp),但下次再越阈值
// 仍会再次进入此函数——这与 PB4 实装期的「TierStuck 后不再触发」是不同
// 的(PB0 占位下没有 Stuck 转移,无防抖)。**PB1 接钩点后跑差分时,
// profileEnabled 被翻 true 但 considerPromotion 仍 no-op,byte-equal 应当
// 仍然成立**(没有真正改变执行行为,只是计数)。
//
//nolint:unused // PB4 实装时填实,PB0 占位保留接口形状。
func (b *Bridge) considerPromotion(proto *bytecode.Proto, pd *ProfileData) {
	// PB0 no-op:占位,留 TierState=TierInterp,不进 try-compile 路径。
	// PB4 实装时按 04 §3.2 完整四路径:
	//   (P1) 已吸收态守卫
	//   (P2/P3) Compilability ≠ CompCompilable → TierStuck
	//   (P4) try-compile + installGibbous
	_ = proto
	_ = pd
}
