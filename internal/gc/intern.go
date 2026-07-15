// String interning and JSHash (06 §9).
//
// Weakly-reachable index (06 §9.2): the string intern table does **not** keep
// strings alive — a string's liveness is decided by reachability from other
// roots; if the string is dead-white at sweep time, it is unlinked from its
// bucket chain.
//
// Hash algorithm: Lua 5.1 JSHash with sampling step (finalized in 06 §9.3,
// bit-for-bit identical to upstream).
package gc

import (
	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/object"
)

// HashString computes Lua 5.1's JSHash with sampling step.
//
// Formula (cf. lstring.c luaS_newlstr):
//
//	h = len
//	step = (len >> 5) + 1
//	for i := len; i >= step; i -= step:
//	    h = h ^ ((h<<5) + (h>>2) + uint32(b[i-1]))
//
// Short strings (≤31 bytes) are hashed byte-by-byte; long strings sample at
// most ~32 bytes (step grows to skip-sample).
func HashString(b []byte) uint32 {
	l := uint32(len(b))
	h := l
	step := (l >> 5) + 1
	for i := l; i >= step; i -= step {
		h ^= (h << 5) + (h >> 2) + uint32(b[i-1])
	}
	return h
}

// Intern returns the GCRef of a String object whose content equals b: reuse on
// hit, otherwise allocate and insert into the table.
//
// Caller contract (06 §9.1): if the allocation triggers a GC, b must stay
// reachable from a root (typically b comes from the Lua stack or a Go slice held
// by the host stack; this function does not push a shadow stack itself).
func (c *Collector) Intern(b []byte) arena.GCRef {
	h := HashString(b)
	bucket := h & c.strMask
	for _, ref := range c.strBuckets[bucket] {
		if c.stringMatches(ref, h, b) {
			return ref
		}
	}
	// Miss: allocate a new String and insert it. This allocation may trigger a
	// GC — see the caller contract.
	ref := object.AllocString(c.a, b, h)
	c.LinkSweep(ref) // link into the sweep chain
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
			// swap-remove (order-independent)
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
