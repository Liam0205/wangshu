// Freelist — size-class 定长桶 + LARGE 首次适配(06 §2)。
//
// 分配两级:freelist 命中(O(1) 弹出复用)→ bump 线性切分。
// 回收(gc sweep / rehash 换段)经 Free 归还:
//   - ≤64 字:按 size-class 入定长桶(分配时已向上取整到桶代表字数,桶内尺寸统一);
//   - >64 字:入 LARGE 侵入式链(word0=next, word1=块字数),首次适配,
//     仅在「精确命中」或「剩余 >64 字可独立成块」时取用(避免桶外碎片浪费)。
//
// 不做 coalescing、不做跨 class 切分(06 §2.2 P1 简化)。
// 复用内存是脏的:构造函数必须显式初始化全部字段,不得依赖 fresh-zero
// (AllocString 的 NUL 填充已改显式清零)。
package arena

import "runtime"

// size-class 划分(06 §2.2):20 桶 + LARGE。
const (
	numSizeClasses      = 20
	largeThresholdWords = 64
)

// sizeClass 把字数(1..64)映射到桶号。
func sizeClass(words uint32) int {
	switch {
	case words <= 8:
		return int(words - 1)
	case words <= 16:
		return int(8 + (words-9)/2)
	case words <= 32:
		return int(12 + (words-17)/4)
	default:
		return int(16 + (words-33)/8)
	}
}

// classWords 返回桶代表字数(桶内块统一按此尺寸分配)。
func classWords(c int) uint32 {
	switch {
	case c < 8:
		return uint32(c + 1)
	case c < 12:
		return uint32(10 + 2*(c-8))
	case c < 16:
		return uint32(20 + 4*(c-12))
	default:
		return uint32(40 + 8*(c-16))
	}
}

// debugFreelist 打开 Free/Alloc 配对断言与 use-after-free 检测(双重释放 /
// 释放区间重叠 / 读写已释放字)。常关;排障时改 true 重编译。
const debugFreelist = false

// FreeSiteOf 返回 debug 模式下记录的释放点(排障用)。
func (a *Arena) FreeSiteOf(ref GCRef) string {
	if a.freeSite == nil {
		return "?"
	}
	return a.freeSite[ref]
}

// Free 把一个此前经 AllocBytes 分配的块归还 freelist。
//
// nbytes 必须等于当时的请求字节数(Free 内部按相同规则取整到实际块尺寸)。
// 调用方(gc sweep / rehash)保证不双重归还、归还后不再经旧 GCRef 访问。
func (a *Arena) Free(ref GCRef, nbytes uint32) {
	if ref.IsNull() {
		return
	}
	need := roundUp8(nbytes)
	if need == 0 {
		need = 8
	}
	words := need / 8
	if debugFreelist {
		if words <= largeThresholdWords {
			words = classWords(sizeClass(words))
		}
		if a.freeSet == nil {
			a.freeSet = map[GCRef]uint32{}
			a.freeSite = map[GCRef]string{}
		}
		for w := uint32(0); w < words; w++ {
			at := ref + GCRef(w*8)
			if _, dup := a.freeSet[at]; dup {
				panic("arena: double free / overlapping free at " + itoa(uint64(at)) + " base " + itoa(uint64(ref)))
			}
		}
		site := callerSite()
		for w := uint32(0); w < words; w++ {
			a.freeSet[ref+GCRef(w*8)] = 1
			a.freeSite[ref+GCRef(w*8)] = site
		}
		words = need / 8
	}
	if words <= largeThresholdWords {
		c := sizeClass(words)
		words = classWords(c)
		a.words[ref>>3] = uint64(a.freeHeads[c])
		a.freeHeads[c] = ref
		a.freeBytes += uint64(words) * 8
		return
	}
	a.pushLarge(ref, words)
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// callerSite 返回 Free 的调用栈摘要(debug 用)。
func callerSite() string {
	var pcs [8]uintptr
	n := runtime.Callers(3, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	out := ""
	for {
		f, more := frames.Next()
		out += f.Function + ":" + itoa(uint64(f.Line)) + " <- "
		if !more {
			break
		}
	}
	return out
}

// pushLarge 把一个 >64 字块挂入 LARGE 链头(word0=next, word1=words)。
func (a *Arena) pushLarge(ref GCRef, words uint32) {
	a.words[ref>>3] = uint64(a.largeHead)
	a.words[(ref>>3)+1] = uint64(words)
	a.largeHead = ref
	a.freeBytes += uint64(words) * 8
}

// popSizeClass 尝试从定长桶弹出一块(块尺寸 = classWords(c))。
func (a *Arena) popSizeClass(c int) GCRef {
	ref := a.freeHeads[c]
	if ref.IsNull() {
		return 0
	}
	a.freeHeads[c] = GCRef(a.words[ref>>3])
	a.freeBytes -= uint64(classWords(c)) * 8
	if debugFreelist {
		for w := uint32(0); w < classWords(c); w++ {
			delete(a.freeSet, ref+GCRef(w*8))
		}
	}
	return ref
}

// popLarge 首次适配。取块条件:精确命中,或剩余可独立利用:
//   - 剩余 > 64 字:独立成 LARGE 块;
//   - 剩余 ∈ [1, 64] 字:向下取整到最近 size-class 桶代表字数入定长桶,
//     桶代表与剩余之间的尾巴(< 下一桶步长,≤7 字)就地丢弃——浪费有界,
//     换取"略大块"不再永久滞留(否则链上长期只有略大块时 freelist 名义
//     有空闲、实际不可达,新分配持续走 bump)。
func (a *Arena) popLarge(needWords uint32) GCRef {
	var prev GCRef
	ref := a.largeHead
	for !ref.IsNull() {
		next := GCRef(a.words[ref>>3])
		bw := uint32(a.words[(ref>>3)+1])
		if bw >= needWords {
			if prev.IsNull() {
				a.largeHead = next
			} else {
				a.words[prev>>3] = uint64(next)
			}
			a.freeBytes -= uint64(bw) * 8
			if debugFreelist {
				for w := uint32(0); w < bw; w++ {
					delete(a.freeSet, ref+GCRef(w*8))
				}
			}
			if rem := bw - needWords; rem > 0 {
				remRef := ref + GCRef(needWords*8)
				if rem > largeThresholdWords {
					if debugFreelist {
						for w := uint32(0); w < rem; w++ {
							a.freeSet[remRef+GCRef(w*8)] = 1
						}
					}
					a.pushLarge(remRef, rem)
				} else {
					// 向下取整入定长桶(floorClass:桶代表 ≤ rem 的最大桶)
					if c := floorClass(rem); c >= 0 {
						a.Free(remRef, classWords(c)*8)
					}
					// rem < 1 桶最小字数不可能(rem ≥ 1 字即 class 0);尾巴丢弃
				}
			}
			return ref
		}
		prev = ref
		ref = next
	}
	return 0
}

// floorClass 返回桶代表字数 ≤ words 的最大 size-class(无则 -1)。
// 入参 >64 字 clamp 到 64(sizeClass 的入参契约是 1..64,越界下标 panic)。
func floorClass(words uint32) int {
	if words == 0 {
		return -1
	}
	if words > largeThresholdWords {
		words = largeThresholdWords
	}
	c := sizeClass(words)
	for c >= 0 && classWords(c) > words {
		c--
	}
	return c
}

// FreeBytes 返回当前 freelist 上的总空闲字节(测试/观测用)。
func (a *Arena) FreeBytes() uint64 { return a.freeBytes }
