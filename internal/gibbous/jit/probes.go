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

// SpecRegKHits 返回当前累计 reg-K 模板编译命中次数。仅测试用。
func SpecRegKHits() uint64 { return atomic.LoadUint64(&specRegKHits) }

// SpecRegRegHits 返回当前累计 reg-reg 模板编译命中次数。仅测试用。
func SpecRegRegHits() uint64 { return atomic.LoadUint64(&specRegRegHits) }

// ResetSpecHits 把所有 spec 命中计数清零(测试开始前调,防之前其它测试
// 残留累积影响断言)。仅测试用。
func ResetSpecHits() {
	atomic.StoreUint64(&specRegKHits, 0)
	atomic.StoreUint64(&specRegRegHits, 0)
}

// incSpecRegKHits 包内 ++(Compile 触发 useSpecRegK 时调)。
func incSpecRegKHits() { atomic.AddUint64(&specRegKHits, 1) }

// incSpecRegRegHits 包内 ++(Compile 触发 useSpec reg-reg 时调)。
func incSpecRegRegHits() { atomic.AddUint64(&specRegRegHits, 1) }
