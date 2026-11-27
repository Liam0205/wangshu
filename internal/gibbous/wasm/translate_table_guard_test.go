//go:build wangshu_p3

package wasm

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/tetratelabs/wazero"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/gibbous/wasm/memadapter"
	"github.com/Liam0205/wangshu/internal/value"
)

// translate_table_guard_test.go runs the inline GETTABLE / SETTABLE / SELF
// paths against hand-laid tables that pass the table guard (the frozen address
// and gen) without being the table the IC slot was filled on: the array part
// is shorter than the frozen index, the hash part has fewer nodes, there is no
// hash part, or the node at the frozen index holds another key.

const (
	guardTblOff   = 1024 // the table head
	guardBlockOff = 4096 // its array or node block
	guardGen      = 7
	guardKey      = uint64(0xFFFB_0000_0000_4242) // a string-tagged constant key
	guardOtherKey = uint64(0xFFFB_0000_0000_4343)
	guardSentinel = uint64(0xDEAD_BEEF_0000_0000)
)

// guardForm is one inline path: the instruction, where the table and the value
// sit, and whether the key is a constant.
type guardForm struct {
	name   string
	node   bool // NodeHit; otherwise ArrayHit
	store  bool // SETTABLE
	regKey bool // the key comes from a register (ArrayHit only)
	ins    func(key int) bytecode.Instruction
	tblReg int // the register holding the table
}

// Registers: R0 destination (GETTABLE / SELF), R1 the table (SETTABLE: R(A)),
// R2 the table for SELF and the stored value for SETTABLE, R3 a register key.
func guardForms() []guardForm {
	k0 := bytecode.MaxK
	return []guardForm{
		{name: "GETTABLE_array_constant_key", tblReg: 1,
			ins: func(int) bytecode.Instruction { return bytecode.EncodeABC(bytecode.GETTABLE, 0, 1, k0) }},
		{name: "GETTABLE_array_register_key", regKey: true, tblReg: 1,
			ins: func(int) bytecode.Instruction { return bytecode.EncodeABC(bytecode.GETTABLE, 0, 1, 3) }},
		{name: "GETTABLE_node", node: true, tblReg: 1,
			ins: func(int) bytecode.Instruction { return bytecode.EncodeABC(bytecode.GETTABLE, 0, 1, k0) }},
		{name: "SETTABLE_array_constant_key", store: true, tblReg: 1,
			ins: func(int) bytecode.Instruction { return bytecode.EncodeABC(bytecode.SETTABLE, 1, k0, 2) }},
		{name: "SETTABLE_array_register_key", store: true, regKey: true, tblReg: 1,
			ins: func(int) bytecode.Instruction { return bytecode.EncodeABC(bytecode.SETTABLE, 1, 3, 2) }},
		{name: "SETTABLE_node", node: true, store: true, tblReg: 1,
			ins: func(int) bytecode.Instruction { return bytecode.EncodeABC(bytecode.SETTABLE, 1, k0, 2) }},
		{name: "SELF_node", node: true, tblReg: 2,
			ins: func(int) bytecode.Instruction { return bytecode.EncodeABC(bytecode.SELF, 0, 2, k0) }},
	}
}

// guardCase is a table handed to code compiled for index. size is the asize
// (array forms) or the node count (node forms) in its head; 0 means the table
// has no such part and its arrayRef / nodeRef is 0. key is what the node at
// index holds.
type guardCase struct {
	index, size uint32
	key         uint64
	hit         bool
}

var guardArrayCases = []guardCase{
	{index: 0, size: 1, hit: true},
	{index: 7, size: 8, hit: true},
	{index: 7, size: 7},
	{index: 7, size: 1},
	{index: 0, size: 0},
}

var guardNodeCases = []guardCase{
	{index: 0, size: 1, key: guardKey, hit: true},
	{index: 3, size: 4, key: guardKey, hit: true},
	{index: 3, size: 4, key: guardOtherKey},
	{index: 3, size: 2, key: guardKey},
	{index: 3, size: 1, key: guardKey},
	// No hash part: hmask is 0 as well, so index 0 passes the size check and
	// only the nodeRef check stands between the code and address 0. The test
	// puts the key in R0 (address 0), so the key at "node 0" matches too.
	{index: 0, size: 0, key: guardKey},
}

func TestPW5_InlineSlotGuards(t *testing.T) {
	old := math.Float64bits(88)
	stored := math.Float64bits(12345)
	tblRaw := uint64(value.TagTable)<<48 | guardTblOff

	for _, f := range guardForms() {
		cases := guardArrayCases
		if f.node {
			cases = guardNodeCases
		}
		for _, cs := range cases {
			t.Run(fmt.Sprintf("%s/index_%d_size_%d_key_%x", f.name, cs.index, cs.size, cs.key&0xFFFF), func(t *testing.T) {
				ctx := context.Background()
				rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigCompiler())
				defer rt.Close(ctx)
				holder, err := memadapter.New(ctx, rt, 64*1024, 1<<31)
				if err != nil {
					t.Fatal(err)
				}
				defer holder.Close()
				mem := holder.Memory()

				// The table: the head records size; the value (and a node's key) is planted where
				// index points whatever the size, so code that does not look at the size finds it.
				block := uint64(guardBlockOff)
				if cs.size == 0 {
					block = 0
				}
				var valueOff uint32
				if f.node {
					hmask := uint64(0)
					if cs.size > 0 {
						hmask = uint64(cs.size - 1)
					}
					mem.WriteUint64Le(guardTblOff+8, hmask<<32)
					mem.WriteUint64Le(guardTblOff+16, 0)
					mem.WriteUint64Le(guardTblOff+24, block)
					valueOff = uint32(block) + cs.index*24 + 8
					if cs.size > 0 {
						mem.WriteUint64Le(uint32(block)+cs.index*24, cs.key)
					}
				} else {
					mem.WriteUint64Le(guardTblOff+8, uint64(cs.size))
					mem.WriteUint64Le(guardTblOff+16, guardBlockOff) // an array address even when asize is 0
					mem.WriteUint64Le(guardTblOff+24, 0)
					valueOff = guardBlockOff + cs.index*8
				}
				mem.WriteUint64Le(guardTblOff+40, uint64(guardGen)<<32)
				if cs.size > 0 || !f.node {
					mem.WriteUint64Le(valueOff, old)
				}

				kind := uint8(bytecode.ICKindArrayHit)
				k0 := value.Value(math.Float64bits(float64(cs.index) + 1))
				if f.node {
					kind = bytecode.ICKindNodeHit
					k0 = value.Value(guardKey)
				}
				proto := &bytecode.Proto{
					Code: []bytecode.Instruction{
						f.ins(0),
						bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
					},
					Consts: []value.Value{k0},
					IC:     make([]bytecode.ICSlot, 2),
				}
				proto.IC[0] = bytecode.ICSlot{Kind: kind, Shape: guardGen, Index: cs.index, TableRef: guardTblOff}

				mh := &mockHost{}
				c := NewCompiler(ctx, rt, mh)
				if !c.SupportsAllOpcodes(proto) {
					t.Fatal("proto should be supported")
				}
				gc, err := c.Compile(proto, nil)
				if err != nil {
					t.Fatalf("Compile: %v", err)
				}
				defer func() { _ = gc.(*p3Code).Dispose() }()

				// Registers. R0 is the destination; in the no-hash-part case it doubles as "node 0".
				regs := []uint64{guardSentinel, guardSentinel, guardSentinel, math.Float64bits(float64(cs.index) + 1)}
				if f.node && cs.size == 0 {
					regs[0] = guardKey
				}
				regs[f.tblReg] = tblRaw
				if f.store {
					regs[2] = stored
				}
				for i, r := range regs {
					mem.WriteUint64Le(uint32(8*i), r)
				}

				if status := gc.(*p3Code).Run(make([]uint64, 1), 0); status != 0 {
					t.Fatalf("run status=%d pendingErr=%v", status, gc.(*p3Code).PendingErr())
				}

				wantRegs := append([]uint64(nil), regs...)
				if !f.store && !f.regKey && f.tblReg == 2 {
					wantRegs[1] = tblRaw // SELF: R(A+1) := R(B) comes before every check
				}
				wantSlot := old
				wantHelper := 1
				if cs.hit {
					wantHelper = 0
					if f.store {
						wantSlot = stored
					} else {
						wantRegs[0] = old
					}
				}
				if mh.tableHelperCalls != wantHelper {
					t.Errorf("helper calls = %d, want %d", mh.tableHelperCalls, wantHelper)
				}
				for i := range wantRegs {
					if got, _ := mem.ReadUint64Le(uint32(8 * i)); got != wantRegs[i] {
						t.Errorf("R(%d) = %#x, want %#x", i, got, wantRegs[i])
					}
				}
				if cs.size > 0 || !f.node {
					if got, _ := mem.ReadUint64Le(valueOff); got != wantSlot {
						t.Errorf("the slot at index %d = %#x, want %#x", cs.index, got, wantSlot)
					}
				}
			})
		}
	}
}
