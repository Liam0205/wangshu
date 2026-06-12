// String interning 与 JSHash(06 §9)。
//
// 弱可达索引(06 §9.2):string intern 表**不延命**——串的存活由其它根可达性决定;
// sweep 时若该串死白,从 bucket 链摘除。
//
// 哈希算法:Lua 5.1 JSHash 分段采样(06 §9.3 定稿,与官方逐位一致)。
package gc

import (
	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/object"
)

// HashString computes Lua 5.1's JSHash with sampling step.
//
// 公式(对照 lstring.c luaS_newlstr):
//
//	h = len
//	step = (len >> 5) + 1
//	for i := len; i >= step; i -= step:
//	    h = h ^ ((h<<5) + (h>>2) + uint32(b[i-1]))
//
// 短串(≤31 字节)逐字节;长串最多采样 ~32 字节(step 增大跳采)。
func HashString(b []byte) uint32 {
	l := uint32(len(b))
	h := l
	step := (l >> 5) + 1
	for i := l; i >= step; i -= step {
		h ^= (h << 5) + (h >> 2) + uint32(b[i-1])
	}
	return h
}

// Intern 返回内容等于 b 的 String 对象 GCRef:命中复用,未命中分配并入表。
//
// 调用方契约(06 §9.1):若分配触发 GC,b 必须从根可达(通常 b 来自 Lua 栈或 host
// 栈持有的 Go 切片,本函数不主动 push shadow stack)。
func (c *Collector) Intern(b []byte) arena.GCRef {
	h := HashString(b)
	bucket := h & c.strMask
	for _, ref := range c.strBuckets[bucket] {
		if c.stringMatches(ref, h, b) {
			return ref
		}
	}
	// 未命中:分配新 String + 入表。这次分配可能触发 GC——见调用方契约。
	ref := object.AllocString(c.a, b, h)
	c.LinkSweep(ref) // 挂入 sweep 链
	c.AllocCharge(object.StringObjectBytes(uint32(len(b))))
	c.strBuckets[bucket] = append(c.strBuckets[bucket], ref)
	c.strCount++
	if c.strCount > c.strMask+1 {
		c.rehashStringTable()
	}
	return ref
}

func (c *Collector) stringMatches(ref arena.GCRef, h uint32, b []byte) bool {
	if object.StringHash(c.a, ref) != h {
		return false
	}
	if object.StringLen(c.a, ref) != uint32(len(b)) {
		return false
	}
	got := object.StringBytes(c.a, ref)
	for i := range got {
		if got[i] != b[i] {
			return false
		}
	}
	return true
}

// removeFromStringTable removes a dead-white string ref from its bucket (called by sweep).
func (c *Collector) removeFromStringTable(ref arena.GCRef) {
	h := object.StringHash(c.a, ref)
	bucket := h & c.strMask
	chain := c.strBuckets[bucket]
	for i, r := range chain {
		if r == ref {
			// swap-remove(顺序无关)
			n := len(chain) - 1
			chain[i] = chain[n]
			c.strBuckets[bucket] = chain[:n]
			c.strCount--
			return
		}
	}
}

// rehashStringTable doubles bucket count and re-distributes all strings.
func (c *Collector) rehashStringTable() {
	newMask := c.strMask*2 + 1
	newBuckets := make([][]arena.GCRef, newMask+1)
	for _, chain := range c.strBuckets {
		for _, ref := range chain {
			h := object.StringHash(c.a, ref)
			b := h & newMask
			newBuckets[b] = append(newBuckets[b], ref)
		}
	}
	c.strBuckets = newBuckets
	c.strMask = newMask
}

// StringTableSize returns the current number of interned strings (diagnostic).
func (c *Collector) StringTableSize() uint32 { return c.strCount }
