package p3indirect

// 手写/生成 Wasm module 二进制(PW10 spike)。三种 driver 形态共享同一 leaf
// 体 + 同一循环骨架,**只差调用机制**,使 dispatch 成本可公平对比:
//
//   - indirect:单 module 内 `call_indirect` 调 leaf(本方案的目标形态)
//   - direct  :单 module 内 `call` 直调 leaf(地板基线,无表查)
//   - host    :`call` 一个 imported Go 函数(= PW0 S3N 形态,~143ns 跨层税基线)
//
// leaf 体统一:`leaf(x) = x*3 + 1`(纯算术,隔离 dispatch 成本——函数体成本
// 在三形态下相同,差异全在调用机制)。driver 在紧循环里累加 leaf(n) N 次,
// 摊掉外层 fn.Call 开销,ns/op ÷ N = 单次 dispatch 摊销成本。
//
// Wasm binary 格式:https://webassembly.github.io/spec/core/binary/

// callKind 区分 driver 的调用机制。
type callKind int

const (
	kindIndirect callKind = iota // call_indirect(本方案)
	kindDirect                   // call(直调地板)
	kindHost                     // call imported(跨层税基线)
)

// buildModule 生成一个 spike module 二进制。
//
//   - numLeaves:本地 leaf 函数数量(≥1)。indirect/direct 用;host 形态忽略
//     (host 无本地 leaf,只 import 一个)。numLeaves>1 仅用于 S-B 给 module
//     灌入更多函数测 CompileModule 随函数数的伸缩。
//   - kind:driver 的调用机制。
//
// 函数索引布局:
//   - indirect/direct:func 0..numLeaves-1 = leaves;func numLeaves = driver。
//   - host:func 0 = imported h_leaf;func 1 = driver。
func buildModule(numLeaves int, kind callKind) []byte {
	if numLeaves < 1 {
		numLeaves = 1
	}
	var b []byte
	b = append(b, 0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00) // magic+version

	// Type section:1 个类型 (i32)->(i32)。
	b = append(b, sec(0x01, concat(
		uleb(1),                              // count = 1
		[]byte{0x60, 0x01, 0x7f, 0x01, 0x7f}, // (i32)->(i32)
	))...)

	driverIdx := uint32(numLeaves) // indirect/direct:driver 在 leaves 之后

	if kind == kindHost {
		// Import section:env.h_leaf : type 0(占 func index 0)。
		b = append(b, sec(0x02, concat(
			uleb(1),
			importFuncEntry("env", "h_leaf", 0),
		))...)
		// Function section:1 个本地函数(driver)。
		b = append(b, sec(0x03, concat(uleb(1), uleb(0)))...)
		driverIdx = 1 // import 占 0,driver 是 1
	} else {
		// Function section:numLeaves 个 leaf + 1 个 driver,全 type 0。
		fn := uleb(uint32(numLeaves + 1))
		for i := 0; i <= numLeaves; i++ {
			fn = append(fn, uleb(0)...)
		}
		b = append(b, sec(0x03, fn)...)
	}

	// Table + Element section:仅 indirect 需要(funcref 表 + 填表)。
	if kind == kindIndirect {
		// Table section:1 张 funcref 表,min=numLeaves。
		b = append(b, sec(0x04, concat(
			uleb(1),                 // count = 1
			[]byte{0x70},            // elemtype funcref
			[]byte{0x00},            // limits flags=0(no max)
			uleb(uint32(numLeaves)), // min
		))...)
	}

	// Export section:export "driver"。
	b = append(b, sec(0x07, concat(
		uleb(1),
		[]byte{0x06}, []byte("driver"),
		[]byte{0x00}, uleb(driverIdx), // kind=func, index
	))...)

	if kind == kindIndirect {
		// Element section:active segment 0,table[0..numLeaves-1] = leaf 0..numLeaves-1。
		el := concat(
			uleb(1),                  // 1 个 segment
			[]byte{0x00},             // flags=0(active, table 0, offset expr)
			[]byte{0x41, 0x00, 0x0b}, // offset = (i32.const 0) end
			uleb(uint32(numLeaves)),  // num funcs
		)
		for i := 0; i < numLeaves; i++ {
			el = append(el, uleb(uint32(i))...)
		}
		b = append(b, sec(0x09, el)...)
	}

	// Code section:leaf 体 ×numLeaves(host 形态 0 个)+ driver 体 ×1。
	var bodies [][]byte
	if kind != kindHost {
		for i := 0; i < numLeaves; i++ {
			bodies = append(bodies, leafBody())
		}
	}
	bodies = append(bodies, driverBody(kind))
	code := uleb(uint32(len(bodies)))
	for _, body := range bodies {
		code = append(code, concat(uleb(uint32(len(body))), body)...)
	}
	b = append(b, sec(0x0a, code)...)

	return b
}

// leafBody — leaf(x) = x*3 + 1。返回 code 段单函数体(localdecls + instrs + end)。
func leafBody() []byte {
	return concat(
		[]byte{0x00}, // 0 个 local 组
		[]byte{
			0x20, 0x00, // local.get 0 ($x)
			0x41, 0x03, // i32.const 3
			0x6c,       // i32.mul
			0x41, 0x01, // i32.const 1
			0x6a, // i32.add
		},
		[]byte{0x0b}, // end
	)
}

// driverBody — driver(n) { acc=0; while n>0 { acc += <call> leaf(n); n-- } return acc }。
// 调用机制按 kind 切换那一小段。
func driverBody(kind callKind) []byte {
	// locals:1 组 1 个 i32($acc = local 1;$n = param local 0)。
	locals := []byte{0x01, 0x01, 0x7f}

	// 调用那一段(栈上已 [acc, n],下面把 leaf(n) 算出再 i32.add 回 acc)。
	var callSeq []byte
	switch kind {
	case kindIndirect:
		callSeq = []byte{
			0x41, 0x00, // i32.const 0   (table index)
			0x11, 0x00, 0x00, // call_indirect type=0 table=0
		}
	case kindDirect:
		callSeq = []byte{0x10, 0x00} // call 0 ($leaf)
	case kindHost:
		callSeq = []byte{0x10, 0x00} // call 0 (imported h_leaf)
	}

	body := concat(
		[]byte{
			0x02, 0x40, // block $exit (void)
			0x03, 0x40, // loop $top (void)
			0x20, 0x00, // local.get 0 ($n)
			0x45,       // i32.eqz
			0x0d, 0x01, // br_if $exit (depth 1)
			0x20, 0x01, // local.get 1 ($acc)
			0x20, 0x00, // local.get 0 ($n) — arg to leaf
		},
		callSeq, // 算 leaf(n) 入栈
		[]byte{
			0x6a,       // i32.add (acc + leaf(n))
			0x21, 0x01, // local.set 1 ($acc)
			0x20, 0x00, // local.get 0 ($n)
			0x41, 0x01, // i32.const 1
			0x6b,       // i32.sub
			0x21, 0x00, // local.set 0 ($n)
			0x0c, 0x00, // br $top (depth 0)
			0x0b,       // end loop
			0x0b,       // end block
			0x20, 0x01, // local.get 1 ($acc) — return value
		},
	)
	return concat(locals, body, []byte{0x0b}) // + end func
}

// --- wasm binary 构造 helper(同 p3boundary modules.go)---

func sec(id byte, payload []byte) []byte {
	return concat([]byte{id}, uleb(uint32(len(payload))), payload)
}

func importFuncEntry(mod, name string, typeIdx uint32) []byte {
	return concat(
		[]byte{byte(len(mod))}, []byte(mod),
		[]byte{byte(len(name))}, []byte(name),
		[]byte{0x00}, uleb(typeIdx), // kind=func, type index
	)
}

func uleb(v uint32) []byte {
	var out []byte
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			c |= 0x80
		}
		out = append(out, c)
		if v == 0 {
			return out
		}
	}
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
