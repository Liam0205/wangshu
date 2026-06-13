package p3boundary

// 三档 spike 样本的 Wasm 模块二进制(手写,对应 WAT 见各常量注释)。
//
// 手写 wasm binary 而非依赖 wat2wasm 外部工具:① 模块极小可精确手写;
// ② 不引入构建期外部工具依赖;③ 字节布局有注释可审查。
//
// Wasm binary 格式参考:https://webassembly.github.io/spec/core/binary/
// magic(4) + version(4) + sections。每 section = id(1) + size(uleb) + payload。

// wasmHeader 是所有模块共享的 magic + version。
//
//	\0asm                    magic
//	01 00 00 00              version 1
var wasmHeader = []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}

// s1Wasm — S1 空往返:一个空函数,无参无返回。
//
// WAT:
//
//	(module
//	  (func (export "noop")))
var s1Wasm = concat(
	wasmHeader,
	// Type section (id=1):1 个类型 [] -> []
	sec(0x01, []byte{
		0x01,             // count = 1
		0x60, 0x00, 0x00, // func type: 0 params, 0 results
	}),
	// Function section (id=3):1 个函数,用 type 0
	sec(0x03, []byte{
		0x01, // count = 1
		0x00, // func 0 -> type 0
	}),
	// Export section (id=7):export "noop" = func 0
	sec(0x07, []byte{
		0x01,                     // count = 1
		0x04, 'n', 'o', 'o', 'p', // name len=4 "noop"
		0x00, 0x00, // kind=func(0), index=0
	}),
	// Code section (id=10):func 0 body = empty
	sec(0x0a, []byte{
		0x01, // count = 1
		0x02, // body size = 2
		0x00, // local decl count = 0
		0x0b, // end
	}),
)

// s2Wasm — S2 带参往返:(param $base i32) (result i32),体内 i64.load+i64.store。
//
// WAT:
//
//	(module
//	  (memory (export "mem") 1)
//	  (func (export "rw") (param $base i32) (result i32)
//	    (i64.store offset=0 (local.get 0)
//	      (i64.load offset=8 (local.get 0)))
//	    (i32.const 0)))
//
// 体内复刻 [02-translation §3.1] MOVE 的最小工作量(一次 load + 一次 store);
// 返回 i32 status=0 复刻 [04-trampoline §2] 入口签名。
var s2Wasm = concat(
	wasmHeader,
	// Type section:1 个类型 [i32] -> [i32]
	sec(0x01, []byte{
		0x01,       // count = 1
		0x60,       // func type
		0x01, 0x7f, // 1 param: i32
		0x01, 0x7f, // 1 result: i32
	}),
	// Function section:func 0 -> type 0
	sec(0x03, []byte{0x01, 0x00}),
	// Memory section (id=5):1 个 memory,min=1 page
	sec(0x05, []byte{
		0x01,       // count = 1
		0x00, 0x01, // limits: flags=0(no max), min=1
	}),
	// Export section:export "mem"=mem0, "rw"=func0
	sec(0x07, []byte{
		0x02,                            // count = 2
		0x03, 'm', 'e', 'm', 0x02, 0x00, // "mem" kind=mem(2) index=0
		0x02, 'r', 'w', 0x00, 0x00, // "rw"  kind=func(0) index=0
	}),
	// Code section:func 0 body
	sec(0x0a, codeSection(s2Body)),
)

// s2WasmMinMax — S2 变体:memory limits min=1 max=4(带 max,供
// WithMemoryCapacityFromMax 预分配,验证固定容量下 grow 不换 buffer)。
var s2WasmMinMax = concat(
	wasmHeader,
	sec(0x01, []byte{
		0x01,
		0x60,
		0x01, 0x7f,
		0x01, 0x7f,
	}),
	sec(0x03, []byte{0x01, 0x00}),
	// Memory section:limits flags=1(有 max),min=1,max=4
	sec(0x05, []byte{
		0x01,
		0x01, 0x01, 0x04, // flags=1, min=1, max=4
	}),
	sec(0x07, []byte{
		0x02,
		0x03, 'm', 'e', 'm', 0x02, 0x00,
		0x02, 'r', 'w', 0x00, 0x00,
	}),
	sec(0x0a, codeSection(s2Body)),
)

// s2Body — S2 函数体(不含 local decl count 与 end,由 codeSection 包裹)。
//
//	local.get 0          ; 20 00       (base, 作 store 的地址)
//	local.get 0          ; 20 00       (base, 作 load 的地址)
//	i64.load offset=8     ; 29 03 08    (align=3, offset=8)
//	i64.store offset=0    ; 37 03 00    (align=3, offset=0)
//	i32.const 0          ; 41 00
var s2Body = []byte{
	0x20, 0x00, // local.get 0  (store addr)
	0x20, 0x00, // local.get 0  (load addr)
	0x29, 0x03, 0x08, // i64.load align=3 offset=8
	0x37, 0x03, 0x00, // i64.store align=3 offset=0
	0x41, 0x00, // i32.const 0
}

// s3Wasm — S3 反向往返:import 一个 env.h_noop,体内 call 它然后返回。
//
// WAT:
//
//	(module
//	  (import "env" "h_noop" (func $h))
//	  (func (export "callout") (call $h)))
var s3Wasm = concat(
	wasmHeader,
	// Type section:1 个类型 [] -> [](import 与 func 共用)
	sec(0x01, []byte{
		0x01,             // count = 1
		0x60, 0x00, 0x00, // [] -> []
	}),
	// Import section (id=2):import env.h_noop : func type 0
	sec(0x02, []byte{
		0x01,                // count = 1
		0x03, 'e', 'n', 'v', // module "env"
		0x06, 'h', '_', 'n', 'o', 'o', 'p', // name "h_noop"
		0x00, 0x00, // kind=func(0), type index=0
	}),
	// Function section:func 0(本地)-> type 0
	sec(0x03, []byte{0x01, 0x00}),
	// Export section:export "callout" = func index 1(import 占 0)
	sec(0x07, []byte{
		0x01,                                    // count = 1
		0x07, 'c', 'a', 'l', 'l', 'o', 'u', 't', // "callout"
		0x00, 0x01, // kind=func(0) index=1
	}),
	// Code section:func body = call $h(import 在 func index 0)
	sec(0x0a, codeSection([]byte{
		0x10, 0x00, // call 0  (import h_noop)
	})),
)

// s3NWasm — S3 变体:callout_n(param $n i32),体内循环 call imported fn N 次。
// 用于摊掉 fn.Call 外层开销,定位**单次 imported dispatch 的真实成本**——
// 这是 [02-translation 摊销模型] 里慢路径助手 T_cross 的关键输入。
//
// WAT:
//
//	(module
//	  (import "env" "h_noop" (func $h))
//	  (func (export "callout_n") (param $n i32)
//	    (block $exit
//	      (loop $top
//	        (br_if $exit (i32.eqz (local.get 0)))
//	        (call $h)
//	        (local.set 0 (i32.sub (local.get 0) (i32.const 1)))
//	        (br $top)))))
var s3NWasm = concat(
	wasmHeader,
	// Type section:2 个类型 —— type0 [] -> [](import h),type1 [i32] -> [](callout_n)
	sec(0x01, []byte{
		0x02,             // count = 2
		0x60, 0x00, 0x00, // type0: [] -> []
		0x60, 0x01, 0x7f, 0x00, // type1: [i32] -> []
	}),
	// Import section:env.h_noop : type0
	sec(0x02, []byte{
		0x01,
		0x03, 'e', 'n', 'v',
		0x06, 'h', '_', 'n', 'o', 'o', 'p',
		0x00, 0x00, // kind=func type0
	}),
	// Function section:本地 func 0 -> type1
	sec(0x03, []byte{0x01, 0x01}),
	// Export section:callout_n = func index 1
	sec(0x07, []byte{
		0x01,
		0x09, 'c', 'a', 'l', 'l', 'o', 'u', 't', '_', 'n',
		0x00, 0x01,
	}),
	// Code section
	sec(0x0a, codeSection([]byte{
		0x02, 0x40, // block $exit
		0x03, 0x40, // loop $top
		0x20, 0x00, // local.get 0
		0x45,       // i32.eqz
		0x0d, 0x01, // br_if $exit
		0x10, 0x00, // call 0 (import h_noop)
		0x20, 0x00, // local.get 0
		0x41, 0x01, // i32.const 1
		0x6b,       // i32.sub
		0x21, 0x00, // local.set 0
		0x0c, 0x00, // br $top
		0x0b, // end loop
		0x0b, // end block
	})),
)

// s4LongLoopWasm — 四项税验证用:一个跑 N 次空循环的函数(模拟 long-running)。
//
// WAT:
//
//	(module
//	  (func (export "loop") (param $n i32)
//	    (block $exit
//	      (loop $top
//	        (br_if $exit (i32.eqz (local.get 0)))
//	        (local.set 0 (i32.sub (local.get 0) (i32.const 1)))
//	        (br $top)))))
var s4LongLoopWasm = concat(
	wasmHeader,
	sec(0x01, []byte{
		0x01,
		0x60, 0x01, 0x7f, 0x00, // [i32] -> []
	}),
	sec(0x03, []byte{0x01, 0x00}),
	sec(0x07, []byte{
		0x01,
		0x04, 'l', 'o', 'o', 'p',
		0x00, 0x00,
	}),
	sec(0x0a, codeSection([]byte{
		0x02, 0x40, // block $exit (void)
		0x03, 0x40, // loop $top (void)
		0x20, 0x00, // local.get 0
		0x45,       // i32.eqz
		0x0d, 0x01, // br_if $exit (depth 1)
		0x20, 0x00, // local.get 0
		0x41, 0x01, // i32.const 1
		0x6b,       // i32.sub
		0x21, 0x00, // local.set 0
		0x0c, 0x00, // br $top (depth 0)
		0x0b, // end loop
		0x0b, // end block
	})),
)

// --- wasm binary 构造 helper ---

// sec 包裹一个 section:id + uleb(len) + payload。
func sec(id byte, payload []byte) []byte {
	return concat([]byte{id}, uleb(uint32(len(payload))), payload)
}

// codeSection 包裹一个 single-func code section payload:
// count=1 + uleb(bodyLen) + localDeclCount(0) + body + end(0x0b)。
func codeSection(body []byte) []byte {
	full := concat([]byte{0x00}, body, []byte{0x0b}) // local decls=0 + body + end
	return concat([]byte{0x01}, uleb(uint32(len(full))), full)
}

// uleb 编码 unsigned LEB128。
func uleb(v uint32) []byte {
	var out []byte
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			b |= 0x80
		}
		out = append(out, b)
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
