//go:build wangshu_p4 && (amd64 || arm64)

package jit

import (
	"fmt"
	"math"
	"runtime"
	"testing"
	"unsafe"
)

// pj4_template_exec_test.go runs the seven single-access table templates as
// machine code against a hand-built arena. The byte-level tests in the arch
// packages say which instructions a template holds; these say what it does to
// a table, in particular to one that is not the table the interpreter saw.

const (
	tmplNil    = uint64(0xFFF8) << 48
	tmplTable  = uint64(0xFFFC) << 48
	tmplKey    = uint64(0xFFFB)<<48 | 0x4242
	tmplDeopt  = uint64(0xFFFCDEAD_DEADBE00)
	tmplMarker = uint64(0x0123_4567_89AB_CDEF)
	tmplGen    = uint32(7)

	tmplHeadWord   = 16    // the table head
	tmplBlockWord  = 64    // its array or node block
	tmplArenaWords = 16384 // room for 4096 nodes after the block start
)

// tmplForm is one template: how to emit it for an index, and which of the
// three accesses it performs.
type tmplForm struct {
	name  string
	node  bool // addresses the hash part
	store bool // SETTABLE: R(0) is the table, R(1) the value to store
	self  bool // SELF: additionally R(2) := R(0)
	noRet bool // falls through on success instead of returning
	emit  func(index uint32) []byte
}

func tmplForms() []tmplForm {
	off := int32(JITContextArenaBaseOffset)
	return []tmplForm{
		{name: "GetTableArrayHit", emit: func(i uint32) []byte {
			return archEmitGetTableArrayHit(nil, 1, 0, tmplGen, i, off, tmplDeopt)
		}},
		{name: "GetTableNodeHit", node: true, emit: func(i uint32) []byte {
			return archEmitGetTableNodeHit(nil, 1, 0, tmplGen, i, tmplKey, off, tmplDeopt)
		}},
		{name: "SetTableArrayHit", store: true, emit: func(i uint32) []byte {
			return archEmitSetTableArrayHit(nil, 0, 1, tmplGen, i, off, tmplDeopt)
		}},
		{name: "SetTableNodeHit", node: true, store: true, emit: func(i uint32) []byte {
			return archEmitSetTableNodeHit(nil, 0, 1, tmplGen, i, tmplKey, off, tmplDeopt)
		}},
		{name: "SelfArrayHit", self: true, emit: func(i uint32) []byte {
			return archEmitSelfArrayHit(nil, 1, 0, tmplGen, i, off, tmplDeopt)
		}},
		{name: "SelfNodeHit", node: true, self: true, emit: func(i uint32) []byte {
			return archEmitSelfNodeHit(nil, 1, 0, tmplGen, i, tmplKey, off, tmplDeopt)
		}},
		{name: "SelfNodeHitNoRet", node: true, self: true, noRet: true, emit: func(i uint32) []byte {
			buf := archEmitSelfNodeHitNoRet(nil, 1, 0, tmplGen, i, tmplKey, off, tmplDeopt)
			return archEmitLoadKReturn(buf, tmplMarker)
		}},
	}
}

// tmplCase is a table handed to a template compiled for index. size is the
// asize (array forms) or the node count (node forms) its head records; 0 means
// the table has no such part at all, and its arrayRef / nodeRef is 0.
type tmplCase struct {
	index, size uint32
	hit         bool
}

var tmplArrayCases = []tmplCase{
	{index: 7, size: 8, hit: true},
	{index: 0, size: 1, hit: true},
	{index: 7, size: 7},
	{index: 7, size: 1},
	{index: 0, size: 0},
	// Offsets an arm64 LDR/STR immediate cannot hold (4096 words and up).
	{index: 4095, size: 4096, hit: true},
	{index: 4096, size: 4097, hit: true},
	{index: 5000, size: 6000, hit: true},
	{index: 8192, size: 8193, hit: true},
	{index: 5000, size: 5000},
}

var tmplNodeCases = []tmplCase{
	{index: 3, size: 4, hit: true},
	{index: 0, size: 1, hit: true},
	{index: 3, size: 2},
	{index: 3, size: 1},
	{index: 0, size: 0},
	// Node 1365 is the first whose value lies past an arm64 LDR/STR immediate
	// while its key does not; from 1366 on both do.
	{index: 1364, size: 2048, hit: true},
	{index: 1365, size: 2048, hit: true},
	{index: 1366, size: 2048, hit: true},
	{index: 3000, size: 4096, hit: true},
	{index: 1365, size: 1024},
}

// tmplArena builds the arena for one case and returns it with the word index
// of the value the template would address. The value (and for a node its key)
// is planted where index points whatever size the head records: when the head
// says the table is smaller, that place is whatever follows the table's block,
// and a template that does not look at the size reads or writes it.
func tmplArena(node bool, c tmplCase, old uint64) (w []uint64, valueWord int) {
	w = make([]uint64, tmplArenaWords)
	for i := range w {
		w[i] = math.Float64bits(float64(1000000 + i)) // every word a number, none nil
	}
	head := w[tmplHeadWord : tmplHeadWord+6]
	for i := range head {
		head[i] = 0
	}
	block := tmplBlockWord
	if c.size == 0 {
		block = 0
	}
	if node {
		hmask := uint32(0)
		if c.size > 0 {
			hmask = c.size - 1
		}
		head[1] = uint64(hmask) << 32
		head[3] = uint64(block * 8)
		w[block+int(c.index)*3] = tmplKey
		valueWord = block + int(c.index)*3 + 1
	} else {
		head[1] = uint64(c.size)
		head[2] = uint64(block * 8)
		valueWord = block + int(c.index)
	}
	head[5] = uint64(tmplGen) << 32
	w[valueWord] = old
	return w, valueWord
}

func TestPJ4Templates_Execute(t *testing.T) {
	old := math.Float64bits(88)
	stored := math.Float64bits(12345)
	sentinel := math.Float64bits(-1)
	table := tmplTable | tmplHeadWord*8

	for _, f := range tmplForms() {
		cases := tmplArrayCases
		if f.node {
			cases = tmplNodeCases
		}
		for _, c := range cases {
			t.Run(fmt.Sprintf("%s/index_%d_size_%d", f.name, c.index, c.size), func(t *testing.T) {
				page, err := archMmapCode(f.emit(c.index))
				if err != nil {
					t.Fatalf("archMmapCode: %v", err)
				}
				defer func() {
					if err := page.Munmap(); err != nil {
						t.Errorf("Munmap: %v", err)
					}
				}()
				if page.Addr() == 0 {
					t.Skip("no executable pages on this platform")
				}

				w, valueWord := tmplArena(f.node, c, old)
				before := append([]uint64(nil), w...)
				stack := []uint64{table, sentinel, sentinel, sentinel}
				if f.store {
					stack[1] = stored
				}
				ctx := NewJITContext()
				ctx.arenaBase = uintptr(unsafe.Pointer(&w[0]))
				ret := archCallJITSpec(page.Addr(), uintptr(unsafe.Pointer(ctx)), uintptr(unsafe.Pointer(&stack[0])))
				runtime.KeepAlive(w)
				runtime.KeepAlive(ctx)

				// What the arena and the stack should look like afterwards.
				wantArena := before
				wantStack := []uint64{table, sentinel, sentinel, sentinel}
				if f.store {
					wantStack[1] = stored
				}
				if f.self {
					wantStack[2] = table // R(A+1) := R(B) comes before every guard
				}
				if c.hit {
					if f.store {
						wantArena = append([]uint64(nil), before...)
						wantArena[valueWord] = stored
					} else {
						wantStack[1] = old
					}
				}

				switch {
				case !c.hit && ret != tmplDeopt:
					t.Errorf("returned %#x, want the deopt code %#x", ret, tmplDeopt)
				case c.hit && f.noRet && ret != tmplMarker:
					t.Errorf("returned %#x, want %#x from the code after the template", ret, tmplMarker)
				case c.hit && ret == tmplDeopt:
					t.Errorf("returned the deopt code on a table the template was compiled for")
				}
				for i := range wantStack {
					if stack[i] != wantStack[i] {
						t.Errorf("R(%d) = %#x, want %#x", i, stack[i], wantStack[i])
					}
				}
				for i := range wantArena {
					if w[i] != wantArena[i] {
						t.Errorf("arena word %d = %#x, want %#x (the addressed value is word %d)",
							i, w[i], wantArena[i], valueWord)
					}
				}
			})
		}
	}
}
