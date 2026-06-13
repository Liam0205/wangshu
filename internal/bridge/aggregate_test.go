// AggregateProfile 单测(P2+ #3 (C) sync.Pool 双表混合方案)。
package bridge

import (
	"sync"
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// TestAggregate_FlushAccumulates 单 State 跑后调 FlushToAggregate 把私有
// 计数累积入全局聚合表。
func TestAggregate_FlushAccumulates(t *testing.T) {
	defer ResetAggregate() // 防本测试污染全局表

	b := NewBridge()
	p := makeProtoWithCode(bytecode.ADD, bytecode.JMP)

	// 模拟回边/入口累计
	pd := b.ProfileOf(p)
	pd.EntryCount = 50
	pd.BackEdge = []uint32{100, 200}

	b.FlushToAggregate()

	agg := AggregateOf(p)
	if got := agg.EntryCount.Load(); got != 50 {
		t.Errorf("agg EntryCount = %d, want 50", got)
	}
	if got := agg.BackEdge[0].Load(); got != 100 {
		t.Errorf("agg BackEdge[0] = %d, want 100", got)
	}
	if got := agg.BackEdge[1].Load(); got != 200 {
		t.Errorf("agg BackEdge[1] = %d, want 200", got)
	}
}

// TestAggregate_MultiStateAccumulation 多 State 并发 Flush 同一 Proto——
// 全局聚合表应正确累加。这是 (C) 方案存在的本质:跨 State 累积让 sync.Pool
// 形态下也能触发升层。
func TestAggregate_MultiStateAccumulation(t *testing.T) {
	defer ResetAggregate()

	p := makeProtoWithCode(bytecode.ADD)
	const nStates = 8
	const perStateEntry = 25

	var wg sync.WaitGroup
	wg.Add(nStates)
	for i := 0; i < nStates; i++ {
		go func() {
			defer wg.Done()
			b := NewBridge()
			pd := b.ProfileOf(p)
			pd.EntryCount = perStateEntry
			b.FlushToAggregate()
		}()
	}
	wg.Wait()

	agg := AggregateOf(p)
	want := uint32(nStates * perStateEntry)
	if got := agg.EntryCount.Load(); got != want {
		t.Errorf("multi-State agg EntryCount = %d, want %d", got, want)
	}
}

// TestAggregate_LoadOrStoreIdempotent AggregateOf 多次调对同 Proto 返同一
// 实例(惰性建 + sync.Map LoadOrStore 原子性)。
func TestAggregate_LoadOrStoreIdempotent(t *testing.T) {
	defer ResetAggregate()

	p := makeProtoWithCode(bytecode.ADD)
	a1 := AggregateOf(p)
	a2 := AggregateOf(p)
	if a1 != a2 {
		t.Errorf("AggregateOf must return same instance for same Proto")
	}
}

// TestAggregate_ConsiderPromotionWithAggregate (C) 模式入口:即便本 State
// EntryCount 远低于阈值,全局聚合表已越阈值时 considerPromotionWithAggregate
// 仍触发升层。这模拟 sync.Pool 短生命期 State 形态——本 State 刚 Reset
// 完只跑了少量,但全局已积累很多。
func TestAggregate_ConsiderPromotionWithAggregate(t *testing.T) {
	defer ResetAggregate()

	b := NewBridge()
	b.SetP3Compiler(dummyCompileP3{})
	p := makeProtoWithCode(bytecode.ADD)
	pd := b.ProfileOf(p)
	pd.Compilable = CompCompilable

	// 模拟全局已累积越阈值(其它 State 此前的 Flush 留下)
	agg := AggregateOf(p)
	agg.EntryCount.Store(HotEntryThreshold + 1)

	// 本 State 只跑了 5 次(远低 200 阈值)
	pd.EntryCount = 5

	b.considerPromotionWithAggregate(p, pd)

	if pd.TierState != TierGibbous {
		t.Errorf("aggregate-driven promotion failed: TierState = %v, want TierGibbous", pd.TierState)
	}
}
