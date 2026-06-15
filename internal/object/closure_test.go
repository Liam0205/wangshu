package object

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/arena"
)

// TestClosureGibbousSlot 验证 slot 缓存编码往返(PW10 零跨界惰性填充 IC):
// 存 slot+1 / 0=未填充,且回写只动 word1 高 16 位、不腐蚀 protoID / nupvals。
func TestClosureGibbousSlot(t *testing.T) {
	a := arena.New(arena.Options{})
	const protoID = 0x0BADF00D
	const nupvals = 3
	cl := AllocLuaClosure(a, protoID, nupvals)

	// 初始未填充。
	if _, ok := ClosureGibbousSlot(a, cl); ok {
		t.Fatalf("fresh closure 应未填充 slot")
	}
	if got := ClosureProtoID(a, cl); got != protoID {
		t.Fatalf("protoID 初始: %#x", got)
	}
	if got := ClosureNUpvals(a, cl); got != nupvals {
		t.Fatalf("nupvals 初始: %d", got)
	}

	// 填充 slot=0(边界:slot 0 编码为 1,须可区分于未填充)。
	SetClosureGibbousSlot(a, cl, 0)
	if got, ok := ClosureGibbousSlot(a, cl); !ok || got != 0 {
		t.Fatalf("slot=0 往返: got=%d ok=%v", got, ok)
	}
	// 填充不腐蚀 protoID / nupvals。
	if got := ClosureProtoID(a, cl); got != protoID {
		t.Fatalf("protoID 被腐蚀: %#x", got)
	}
	if got := ClosureNUpvals(a, cl); got != nupvals {
		t.Fatalf("nupvals 被腐蚀: %d", got)
	}

	// 改填 slot=8191(maxTableSlots-1,生产上界)。
	SetClosureGibbousSlot(a, cl, 8191)
	if got, ok := ClosureGibbousSlot(a, cl); !ok || got != 8191 {
		t.Fatalf("slot=8191 往返: got=%d ok=%v", got, ok)
	}
	if got := ClosureProtoID(a, cl); got != protoID {
		t.Fatalf("protoID 被腐蚀(改填后): %#x", got)
	}

	// 超 16 位编码域:不缓存(保持原值不变)。
	SetClosureGibbousSlot(a, cl, 0xffff)
	if got, ok := ClosureGibbousSlot(a, cl); !ok || got != 8191 {
		t.Fatalf("超域写应 no-op,保持 8191: got=%d ok=%v", got, ok)
	}

	// upvalue 槽不受 slot 缓存影响(高位与 upvalRef[] 物理隔离)。
	uvRef := arena.GCRef(0xABCD8)
	SetClosureUpvalRef(a, cl, 1, uvRef)
	if got := ClosureUpvalRef(a, cl, 1); got != uvRef {
		t.Fatalf("upvalRef[1]: %#x", got)
	}
	if got, ok := ClosureGibbousSlot(a, cl); !ok || got != 8191 {
		t.Fatalf("写 upval 后 slot 应不变: got=%d ok=%v", got, ok)
	}
}
