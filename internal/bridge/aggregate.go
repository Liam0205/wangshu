// AggregateProfile — Proto 旁全局聚合表(P2+ #3 (C) sync.Pool 双表混合方案,
// `docs/design/p2-bridge/01-profiling.md` §6.4 设计骨架)。
//
// 解决 sync.Pool 短生命期 State 形态下 (B) 方案的退化:每请求新 State
// + Pool 复用 → profileTable 频繁 Reset 清空 → 热度信号永远累不到阈值,
// 升层从不触发。
//
// 形态:
//   - 主表:State 私有 profileTable(B 方案;高频路径 OnBackEdge / OnEnter
//     直接写,无锁无 atomic);
//   - 旁聚合表(本文件):Proto 维度全局共享,atomic.Uint32 计数器,跨
//     State 累积。State.Reset / Bridge 显式调 FlushToAggregate 时把私有
//     表数据合并入聚合表。
//
// 当前实装:接口预设占位 + 真聚合可启用(本文件已完成真聚合实装,但
// State.Reset 路径暂未自动调 FlushToAggregate——sync.Pool 形态在 wangshu
// 公共 API 落地后再接通)。
package bridge

import (
	"sync"
	"sync/atomic"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// AggregateProfile 是单 Proto 的跨 State 聚合数据。
//
// 所有计数器是 atomic——多 State 并发 Flush 时无 race。聚合表与 State
// 私有 profileTable 字段对偶:私有用普通 uint32 写(高频快路径),全局
// 用 atomic.Uint32(低频聚合路径)。
type AggregateProfile struct {
	EntryCount atomic.Uint32 // 跨 State 累计入口数
	// BackEdge 按 pc 索引的回跳计数。延迟分配:首次 Flush 时按
	// len(Proto.Code) 一次性建表(与 ProfileData.BackEdge 同款延迟分配语义,
	// 01 §2.3)。
	BackEdge []atomic.Uint32
}

// aggregateRegistry 全局 Proto 维度聚合表(包级 var,跨所有 Bridge 实例
// 共享)。sync.Map 提供「读多写少」的优化形态——典型场景是大量 OnEnter /
// OnBackEdge 不写聚合表(只写 State 私有);只在 Reset 时偶尔写。
//
// 内存:每个 Proto 一项 AggregateProfile,与 Proto 同生命期;Proto 被 GC
// 时 sync.Map 项不会自动删除(Go 标准库 sync.Map 无 finalizer 钩子)——
// 这是已知的轻微泄漏(每 Proto 几十字节);若实测发现累积量大,改用 weak
// reference 或 Program 析构钩子。
var aggregateRegistry sync.Map // map[*bytecode.Proto]*AggregateProfile

// AggregateOf 取 Proto 的全局聚合表(惰性建表)。
//
// 多 State 并发 OK——sync.Map.LoadOrStore 是 atomic 的;首次调用建表,
// 之后所有 State 共享同一份。
func AggregateOf(proto *bytecode.Proto) *AggregateProfile {
	if v, ok := aggregateRegistry.Load(proto); ok {
		return v.(*AggregateProfile)
	}
	agg := &AggregateProfile{
		BackEdge: make([]atomic.Uint32, len(proto.Code)),
	}
	actual, _ := aggregateRegistry.LoadOrStore(proto, agg)
	return actual.(*AggregateProfile)
}

// FlushToAggregate 把 State 私有 ProfileData 的计数累积入 Proto 旁全局聚合
// 表。调用时机:State.Reset 准备归还 Pool 之前。
//
// 性质:
//   - 不清 State 私有 ProfileData(调用方决定 Reset 策略,01 §6.4.1
//     方案 (C) 推荐「全清 + 累积入聚合表」,但本函数只负责后半段)。
//   - atomic.AddUint32 保证多 State 并发 Flush 无 race。
//   - 不传染状态机(TierState / Compilable):聚合表只关心「热度信号」,
//     状态机字段是 State 私有 / Proto 共享(后者跨 State 一次写),
//     不参与跨 State 累积。
func (b *Bridge) FlushToAggregate() {
	for proto, pd := range b.profileTable {
		if pd.EntryCount == 0 && len(pd.BackEdge) == 0 {
			continue // 该 Proto 在本 State 上没有累积,跳过
		}
		agg := AggregateOf(proto)
		if pd.EntryCount > 0 {
			agg.EntryCount.Add(pd.EntryCount)
		}
		for pc, c := range pd.BackEdge {
			if c > 0 && pc < len(agg.BackEdge) {
				agg.BackEdge[pc].Add(c)
			}
		}
	}
}

// ResetAggregate 清空全局聚合表(测试用——避免测试间状态串台)。
// 生产环境不应调用;聚合表与 Program 同生命期,Program 销毁后 GC 回收。
func ResetAggregate() {
	aggregateRegistry = sync.Map{}
}

// considerPromotionWithAggregate 升层决策的「双表混合」版本(P2+ #3 (C)):
// 在 considerPromotion 之前先查全局聚合表,若聚合表的累计已越阈值,
// 即便 State 私有计数没越也触发升层尝试。
//
// **未在主 considerPromotion 路径自动启用**——保持 (B) 默认形态稳定;
// sync.Pool 形态用户可显式调本函数(或 Bridge 加 EnableAggregateMode 切换,
// 待 wangshu 公共 API 落地)。
//
//nolint:unused // 留 wangshu 公共 API 接通时启用;当前接口预设占位。
func (b *Bridge) considerPromotionWithAggregate(proto *bytecode.Proto, pd *ProfileData, onMain bool) {
	if pd.TierState != TierInterp {
		return
	}
	// 查全局聚合表
	agg := AggregateOf(proto)
	aggEntry := agg.EntryCount.Load()
	var aggMaxBack uint32
	for i := range agg.BackEdge {
		if c := agg.BackEdge[i].Load(); c > aggMaxBack {
			aggMaxBack = c
		}
	}
	// 任一(本地或全局)越阈值即触发——这让 sync.Pool 形态下,即使本
	// State 计数刚清空,全局累积仍能驱动升层。
	if pd.EntryCount >= HotEntryThreshold ||
		pd.MaxBackEdge() >= HotBackEdgeThreshold ||
		aggEntry >= HotEntryThreshold ||
		aggMaxBack >= HotBackEdgeThreshold {
		b.considerPromotion(proto, pd, onMain)
	}
}
