// Arith IC 双计数挪用(`docs/design/p2-bridge/02-ic-feedback.md` §3)。
//
// P1 写不读纯供料(05 §6.4):算术快路径 IsNumber(b) && IsNumber(c) 现场判,
// 不读 IC slot 决定走哪条路。算术 IC 是纯旁路写——P2 聚合器读取此处计数
// 算 confidence(numHits / (numHits+metaHits))。
//
// 字段挪用规约(02 §3.2 + bytecode/proto.go ICSlot 注释):
//   - Shape    = numHits  (快路径双 number 命中数)
//   - Index    = metaHits (慢路径 string coercion / 元方法命中数)
//   - TableRef = 留空恒 0(算术无表无身份)
//   - Kind     = 0 未观测 / 1 已观测过(P2 表示「已被执行,可读双计数」)
//
// **饱和不回绕**(02 §3.3 + 不变式 6):numHits / metaHits 接近 2^32 时停止
// 递增,避免某超热点跑 2^32 次后 confidence 突变。饱和值 2^32-1 对
// `numHits/total` 比例的影响完全可忽略。
package crescent

import (
	"github.com/Liam0205/wangshu/internal/bytecode"
)

const arithCounterSaturation uint32 = ^uint32(0) // 2^32-1,饱和上限

// recordArithNumHit 算术快路径双 number 命中(02 §3.3)。
//
//go:nosplit
func recordArithNumHit(s *bytecode.ICSlot) {
	if s.Shape != arithCounterSaturation {
		s.Shape++
	}
	s.Kind = 1 // 已观测(算术 IC 上 Kind∈{0,1};表 IC kind∈{1,2,3,4})
}

// recordArithMetaHit 算术慢路径 string coercion / 元方法命中(02 §3.3)。
//
//go:nosplit
func recordArithMetaHit(s *bytecode.ICSlot) {
	if s.Index != arithCounterSaturation {
		s.Index++
	}
	s.Kind = 1
}
