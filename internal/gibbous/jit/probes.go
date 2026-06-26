//go:build wangshu_p4

package jit

import "sync/atomic"

// probes.go —— P4 jit 包内白盒命中计数器(承 llmdoc/guides/prove-the-
// path-under-test §4 正向侧解药:测试经 SpecRegKHits() 断言 reg-K 模板
// 真被 Compile 出来,而非降级 host helper 慢路径)。
//
// 生产无功能含义,atomic 单调递增成本可忽略(每次 Compile 一次,远低于
// Compile 自身 µs 级时间)。仅测试读。

// specRegKHits 是 reg-K 投机模板编译命中次数。每次 Compile 走 useSpecRegK
// 分支时 ++1。SpecRegKHits() / ResetSpecRegKHits() 是测试公共接口。
var specRegKHits uint64

// specRegRegHits 是 reg-reg 投机模板编译命中次数(对照 reg-K)。
var specRegRegHits uint64

// specChainHits 是二段链式 chain-KK 投机模板编译命中次数。
var specChainHits uint64

// specForLoopHits 是 PJ3 FORLOOP 字节级 inline 编译命中次数(空 body
// 全常量形态)。
var specForLoopHits uint64

// specTableHits 是 PJ4 表 IC ArrayHit 字节级 inline 编译命中次数。
var specTableHits uint64

// SpecRegKHits 返回当前累计 reg-K 模板编译命中次数。仅测试用。
func SpecRegKHits() uint64 { return atomic.LoadUint64(&specRegKHits) }

// SpecRegRegHits 返回当前累计 reg-reg 模板编译命中次数。仅测试用。
func SpecRegRegHits() uint64 { return atomic.LoadUint64(&specRegRegHits) }

// SpecChainHits 返回当前累计 chain-KK 模板编译命中次数。仅测试用。
func SpecChainHits() uint64 { return atomic.LoadUint64(&specChainHits) }

// SpecForLoopHits 返回当前累计 FORLOOP 模板编译命中次数。仅测试用。
func SpecForLoopHits() uint64 { return atomic.LoadUint64(&specForLoopHits) }

// SpecTableHits 返回当前累计 IC ArrayHit 模板编译命中次数。仅测试用。
func SpecTableHits() uint64 { return atomic.LoadUint64(&specTableHits) }

// ResetSpecHits 把所有 spec 命中计数清零(测试开始前调,防之前其它测试
// 残留累积影响断言)。仅测试用。
func ResetSpecHits() {
	atomic.StoreUint64(&specRegKHits, 0)
	atomic.StoreUint64(&specRegRegHits, 0)
	atomic.StoreUint64(&specChainHits, 0)
	atomic.StoreUint64(&specForLoopHits, 0)
	atomic.StoreUint64(&specTableHits, 0)
}

// incSpecRegKHits 包内 ++(Compile 触发 useSpecRegK 时调)。
func incSpecRegKHits() { atomic.AddUint64(&specRegKHits, 1) }

// incSpecRegRegHits 包内 ++(Compile 触发 useSpec reg-reg 时调)。
func incSpecRegRegHits() { atomic.AddUint64(&specRegRegHits, 1) }

// incSpecChainHits 包内 ++(Compile 触发 useSpecChain 时调)。
func incSpecChainHits() { atomic.AddUint64(&specChainHits, 1) }

// incSpecForLoopHits 包内 ++(Compile 触发 FORLOOP inline 时调)。
func incSpecForLoopHits() { atomic.AddUint64(&specForLoopHits, 1) }

// incSpecTableHits 包内 ++(Compile 触发 IC ArrayHit inline 时调)。
func incSpecTableHits() { atomic.AddUint64(&specTableHits, 1) }
