package p3frame

// PW10「零跨界」里程碑 Stage 0 spike:验证生死假设——
// Wasm 侧帧建拆(段字写 + ciDepth 字增减 + maxOpenIdx 守卫)是否真比当前
// 单 h_call + 单 h_return 两次 host 跨界更快(扣除 Wasm 内记账开销)。
//
// 两 driver 形态共享同一 leaf 体 + 同一循环骨架,**只差「每次调用如何建拆帧」**:
//
//   - inwasm  :call_indirect 调 leaf;建帧(写 4 段字 + 增 ciDepth)与拆帧
//     (读 maxOpenIdx 守卫 + 减 ciDepth + 重载)全在 Wasm 内做,热路径零 Go 跨界。
//     = 本里程碑要兑现的形态。
//   - twocross:call h_call(imported Go,建帧)→ call_indirect 调 leaf → leaf 返回
//     → call h_return(imported Go,拆帧)。= 当前 R3.5 后 gibbous→gibbous 形态
//     (每调 2 次 host 跨界)。
//
// 两形态的「帧建拆工作量」刻意等价(都写同样 4 段字 + 调整 ciDepth + maxOpenIdx
// 守卫),差异**只在这工作在 Wasm 内做还是经 host 跨界回 Go 做**——隔离「跨界本身」
// 的成本。leaf 体统一 `leaf(x)=x*3+1`(同 p3indirect,使 dispatch 基线可比)。
//
// 共享 linear memory 布局(env.memory,driver 入参 base = 帧 0 的字节基址):
//   word @ ciDepthOff   : ciDepth(帧深度游标)
//   word @ maxOpenOff   : maxOpenIdx(开放 upvalue 守卫;spike 恒置高使快路径恒过)
//   段 @ segBase + depth*ciWords*8 : 每帧 4 word(base/funcIdx/top/packed)
//
// Wasm binary 格式:https://webassembly.github.io/spec/core/binary/

const (
	ciWords        = 4      // 每帧 4 word(对齐生产 R2 段布局)
	segBase        = 256    // CallInfo 段起始字节偏移(避开前面的标志字)
	ciDepthOff     = 8      // ciDepth 字字节偏移
	maxOpenOff     = 16     // maxOpenIdx 字字节偏移
	segBaseWordOff = 24     // segBase 镜像字字节偏移(guarded 形态从此现读段基址)
	leafN          = 100000 // driver 紧循环调 leaf 次数(摊外层 fn.Call)
)

// driverKind 区分两形态。
type driverKind int

const (
	kindInwasm   driverKind = iota // 建拆帧全 Wasm 内
	kindTwocross                   // 建拆帧经 h_call/h_return 两次 host 跨界
	kindGuarded                    // 建拆帧全 Wasm 内 + 真实运行期守卫(读 segBase/maxOpen 字 + 守卫分支)
)

// buildFrameModule 生成 spike module 二进制。
//
// 函数索引布局:
//
//	func 0 = imported host.h_call  (i32 depth)->(i32)     —— 建帧(twocross 用)
//	func 1 = imported host.h_return(i32 depth)->()        —— 拆帧(twocross 用)
//	func 2 = leaf(x)=x*3+1
//	func 3 = driver_inwasm(base)
//	func 4 = driver_twocross(base)
//
// (两 driver 都编进同一 module;两 imported host 恒声明,inwasm 形态不调它们。)
func buildFrameModule() []byte {
	var b []byte
	b = append(b, 0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00) // magic+version

	// Type section:
	//   type0 (i32)->(i32)  leaf / driver / h_call
	//   type1 (i32)->()     h_return
	b = append(b, sec(0x01, concat(
		uleb(2),
		[]byte{0x60, 0x01, 0x7f, 0x01, 0x7f}, // type0
		[]byte{0x60, 0x01, 0x7f, 0x00},       // type1
	))...)

	// Import section:env.memory + host.h_call(type0) + host.h_return(type1)。
	b = append(b, sec(0x02, concat(
		uleb(3),
		importMemEntry("env", "memory"),
		importFuncEntry("host", "h_call", 0),
		importFuncEntry("host", "h_return", 1),
	))...)

	// Function section:leaf + driver_inwasm + driver_twocross + driver_guarded(全 type0)。
	b = append(b, sec(0x03, concat(uleb(4), uleb(0), uleb(0), uleb(0), uleb(0)))...)

	// Table section:1 张 funcref 表 min=1(放 leaf 供 call_indirect)。
	b = append(b, sec(0x04, concat(
		uleb(1), []byte{0x70}, []byte{0x00}, uleb(1),
	))...)

	// leaf 的 func index = 2(import 2 个 func 占 0/1)。driver_inwasm=3, driver_twocross=4, driver_guarded=5。
	const leafIdx = 2
	const driverInwasmIdx = 3
	const driverTwocrossIdx = 4
	const driverGuardedIdx = 5

	// Export section:三 driver。
	b = append(b, sec(0x07, concat(
		uleb(3),
		[]byte{0x0d}, []byte("driver_inwasm"), []byte{0x00}, uleb(driverInwasmIdx),
		[]byte{0x0f}, []byte("driver_twocross"), []byte{0x00}, uleb(driverTwocrossIdx),
		[]byte{0x0e}, []byte("driver_guarded"), []byte{0x00}, uleb(driverGuardedIdx),
	))...)

	// Element section:table[0] = leaf。
	b = append(b, sec(0x09, concat(
		uleb(1), []byte{0x00},
		[]byte{0x41, 0x00, 0x0b}, // offset (i32.const 0) end
		uleb(1), uleb(leafIdx),
	))...)

	// Code section:leaf + driver_inwasm + driver_twocross + driver_guarded。
	bodies := [][]byte{
		leafBody(),
		driverBody(kindInwasm),
		driverBody(kindTwocross),
		driverBody(kindGuarded),
	}
	code := uleb(uint32(len(bodies)))
	for _, body := range bodies {
		code = append(code, concat(uleb(uint32(len(body))), body)...)
	}
	b = append(b, sec(0x0a, code)...)
	return b
}

// leafBody — leaf(x) = x*3 + 1(同 p3indirect,纯算术隔离 dispatch 成本)。
func leafBody() []byte {
	return concat(
		[]byte{0x00},
		[]byte{
			0x20, 0x00, // local.get 0
			0x41, 0x03, // i32.const 3
			0x6c,       // i32.mul
			0x41, 0x01, // i32.const 1
			0x6a, // i32.add
		},
		[]byte{0x0b},
	)
}

// driverBody — driver(base) { acc=0; for n=leafN..1 { acc += <建帧><call leaf(n)><拆帧> }; return acc }
//
// inwasm:建拆帧全 Wasm 内(段字写 + ciDepth 增减 + maxOpenIdx 守卫)。
// twocross:建帧 call h_call、拆帧 call h_return(各一次 host 跨界)。
func driverBody(kind driverKind) []byte {
	// locals:$acc(local 1)、$n(local 2)。param $base = local 0。
	locals := []byte{0x01, 0x02, 0x7f} // 1 组 2 个 i32

	// 初始化 acc=0, n=leafN。
	pre := concat(
		[]byte{0x41, 0x00, 0x21, 0x01}, // i32.const 0; local.set $acc
		i32ConstSeq(leafN),             // i32.const leafN
		[]byte{0x21, 0x02},             // local.set $n
	)

	// 循环体:每次迭代建帧 → leaf(n) → 拆帧 → acc += 结果。
	var buildSeq, teardownSeq []byte
	switch kind {
	case kindInwasm:
		buildSeq = inwasmBuild()
		teardownSeq = inwasmTeardown()
	case kindTwocross:
		buildSeq = twocrossBuild()
		teardownSeq = twocrossTeardown()
	case kindGuarded:
		buildSeq = guardedBuild()
		teardownSeq = guardedTeardown()
	}

	loop := concat(
		[]byte{0x02, 0x40, 0x03, 0x40},       // block $exit; loop $top
		[]byte{0x20, 0x02, 0x45, 0x0d, 0x01}, // local.get $n; i32.eqz; br_if $exit
		buildSeq,                             // 建帧(消栈中性)
		[]byte{
			0x20, 0x01, // local.get $acc
			0x20, 0x02, // local.get $n  (leaf arg)
			0x41, 0x00, // i32.const 0   (table index)
			0x11, 0x00, 0x00, // call_indirect type0 table0 → leaf(n)
			0x6a,       // i32.add (acc + leaf(n))
			0x21, 0x01, // local.set $acc
		},
		teardownSeq, // 拆帧(消栈中性)
		[]byte{
			0x20, 0x02, 0x41, 0x01, 0x6b, 0x21, 0x02, // n = n - 1
			0x0c, 0x00, // br $top
			0x0b, 0x0b, // end loop; end block
			0x20, 0x01, // local.get $acc (return)
		},
	)
	return concat(locals, pre, loop, []byte{0x0b})
}

// --- inwasm 形态:建拆帧全 Wasm 内 ---

// inwasmBuild — 建帧:depth = load(ciDepthOff); 写 4 段字 @ segBase+depth*32;
// store(ciDepthOff, depth+1)。模拟 enterLuaFrame 的稳态快路径(无 grow/vararg)。
//
// 段字地址 = segBase + depth*ciWords*8。spike 用定深(driver 帧 + leaf 帧),
// depth 实际在 0/1 间摆动,地址算术与生产同构(depth*32 + segBase)。
func inwasmBuild() []byte {
	// 读 depth 到栈,算段基址 segBase+depth*32,写 4 个 i64 字(值用常量占位,
	// 模拟 base/funcIdx/top/packed 的写入成本),再 ciDepth++。
	// addr = segBase + depth*32
	return concat(
		// --- 写段字 word0..3(地址 = segBase + depth*32 + w*8)---
		segWordStore(0, 0x11), // word0 = 0x11 (模拟 base|funcIdx)
		segWordStore(1, 0x22), // word1 = 0x22 (模拟 top|pc)
		segWordStore(2, 0x33), // word2 = 0x33 (模拟 packed flags)
		segWordStore(3, 0x44), // word3 = 0x44 (模拟 cl)
		// --- ciDepth++ ---
		[]byte{0x41, ciDepthOff}, // i32.const ciDepthOff (addr)
		[]byte{0x41, ciDepthOff}, // i32.const ciDepthOff (再压一次作 load 地址)
		[]byte{0x28, 0x02, 0x00}, // i32.load align=2 offset=0 → depth
		[]byte{0x41, 0x01, 0x6a}, // i32.const 1; i32.add → depth+1
		[]byte{0x36, 0x02, 0x00}, // i32.store align=2 offset=0
	)
}

// inwasmTeardown — 拆帧:读 maxOpenIdx 守卫(spike 恒过)+ ciDepth--。
// 模拟 DoReturn 的稳态快路径(无开放 upvalue、无多值)。
func inwasmTeardown() []byte {
	return concat(
		// --- maxOpenIdx 守卫:if load(maxOpenOff) != 0 then (这里 spike 恒置 0 → 不进)---
		// 读一次模拟守卫的 i32.load 成本(结果 drop,spike 不真分支)。
		[]byte{0x41, maxOpenOff}, // i32.const maxOpenOff
		[]byte{0x28, 0x02, 0x00}, // i32.load
		[]byte{0x1a},             // drop(守卫读取成本,恒过不分支)
		// --- ciDepth-- ---
		[]byte{0x41, ciDepthOff}, // addr
		[]byte{0x41, ciDepthOff}, // load addr
		[]byte{0x28, 0x02, 0x00}, // i32.load → depth
		[]byte{0x41, 0x01, 0x6b}, // i32.const 1; i32.sub → depth-1
		[]byte{0x36, 0x02, 0x00}, // i32.store
	)
}

// --- guarded 形态:建拆帧全 Wasm 内 + 真实运行期守卫(模拟生产 Stage 2/3 守卫开销)---
//
// 与 inwasm 的差异:段基址从 segBase 字**现读**(模拟生产 CI 段可重定位,Wasm 读
// ciSegBaseRef 字现算地址,而非烧立即数);拆帧含**真实 maxOpenIdx 守卫分支**(读字
// 比较 + if,模拟「无开放 upvalue 才走快路径」);建帧含**caller gibbous 位检查**
// (读段帧 word2 bit50 模拟「caller 是 gibbous 才内联」)。守卫恒过(spike 置字使
// 快路径恒走),量「带完整运行期守卫的内联帧建拆」是否仍显著快过 2 跨界。

// segWordStoreVar — 同 segWordStore 但段基址从 segBase 字现读(非常量 segBase)。
func segWordStoreVar(w int, val byte) []byte {
	return concat(
		// addr = load(segBaseWordOff) + depth*32 + w*8
		[]byte{0x41, segBaseWordOff},   // i32.const segBaseWordOff
		[]byte{0x28, 0x02, 0x00},       // i32.load → segBase(现读)
		[]byte{0x41, ciDepthOff},       // i32.const ciDepthOff
		[]byte{0x28, 0x02, 0x00},       // i32.load → depth
		[]byte{0x41, 0x20, 0x6c},       // i32.const 32; i32.mul → depth*32
		[]byte{0x6a},                   // add → segBase + depth*32
		i32ConstSeq(w*8), []byte{0x6a}, // + w*8
		[]byte{0x42, val},        // i64.const val
		[]byte{0x37, 0x03, 0x00}, // i64.store
	)
}

// guardedBuild — 建帧 + caller gibbous 位检查(读段帧 word2 现判)。
func guardedBuild() []byte {
	return concat(
		// caller gibbous 位检查:读 depth 帧的 word2(此处 depth 是 caller),
		// 取 bit50 模拟「caller 是 gibbous 才走内联」(spike 恒真)。
		// addr = load(segBase) + depth*32 + 16(word2)
		[]byte{0x41, segBaseWordOff}, []byte{0x28, 0x02, 0x00}, // load segBase
		[]byte{0x41, ciDepthOff}, []byte{0x28, 0x02, 0x00}, // load depth
		[]byte{0x41, 0x20, 0x6c}, []byte{0x6a}, // depth*32 + segBase
		[]byte{0x41, 0x10, 0x6a}, // + 16 (word2 offset)
		[]byte{0x29, 0x03, 0x00}, // i64.load → word2
		[]byte{0x42, 0x00},       // i64.const 0(spike:不真测 bit50,读取 + drop 量成本)
		[]byte{0x84},             // i64.or(消费 word2 + 0,留结果)
		[]byte{0x1a},             // drop
		// 段字写(段基址现读)+ ciDepth++
		segWordStoreVar(0, 0x11), segWordStoreVar(1, 0x22),
		segWordStoreVar(2, 0x33), segWordStoreVar(3, 0x44),
		[]byte{0x41, ciDepthOff}, []byte{0x41, ciDepthOff},
		[]byte{0x28, 0x02, 0x00}, []byte{0x41, 0x01, 0x6a}, []byte{0x36, 0x02, 0x00},
	)
}

// guardedTeardown — 拆帧 + 真实 maxOpenIdx 守卫分支(读字 + if,恒过)。
func guardedTeardown() []byte {
	return concat(
		// maxOpenIdx 守卫:if load(maxOpenOff) != 0 { (慢路径,spike 恒不进) }
		[]byte{0x41, maxOpenOff}, []byte{0x28, 0x02, 0x00}, // load maxOpenIdx
		[]byte{0x04, 0x40}, // if (void)  —— 真实守卫分支(恒假,不进)
		// 慢路径体(spike 空,生产是 h_return 回退)。
		[]byte{0x0b}, // end if
		// ciDepth--(段基址现读不影响 ciDepth 字,直接减)
		[]byte{0x41, ciDepthOff}, []byte{0x41, ciDepthOff},
		[]byte{0x28, 0x02, 0x00}, []byte{0x41, 0x01, 0x6b}, []byte{0x36, 0x02, 0x00},
	)
}

// --- twocross 形态:建拆帧经 host 跨界 ---

// twocrossBuild — call h_call(depth_placeholder)。h_call(Go)做等价建帧工作。
// 传一个 i32 占位(模拟 base),h_call 返回 i32(模拟刷新后 base,这里 drop)。
func twocrossBuild() []byte {
	return concat(
		[]byte{0x41, 0x00}, // i32.const 0 (placeholder arg)
		[]byte{0x10, 0x00}, // call h_call (imported func 0)
		[]byte{0x1a},       // drop 返回值
	)
}

// twocrossTeardown — call h_return(depth_placeholder)。h_return(Go)做等价拆帧工作。
func twocrossTeardown() []byte {
	return concat(
		[]byte{0x41, 0x00}, // i32.const 0 (placeholder arg)
		[]byte{0x10, 0x01}, // call h_return (imported func 1)
	)
}

// segWordStore — i64.store memory[segBase + depth*32 + w*8] = const val。
// depth 从 ciDepthOff 现 load。栈中性(自压地址 + 值,store 消两个)。
func segWordStore(w int, val byte) []byte {
	// addr = segBase + depth*32 + w*8 ;  depth = load(ciDepthOff)
	return concat(
		// 压 store 地址:segBase + w*8 + depth*32
		[]byte{0x41, ciDepthOff}, // i32.const ciDepthOff
		[]byte{0x28, 0x02, 0x00}, // i32.load → depth
		[]byte{0x41, 0x20},       // i32.const 32 (ciWords*8)
		[]byte{0x6c},             // i32.mul → depth*32
		i32ConstSeq(segBase+w*8), // i32.const (segBase + w*8)
		[]byte{0x6a},             // i32.add → 最终地址
		// 压值
		[]byte{0x42, val}, // i64.const val (单字节 sleb,val<64)
		// store(align=3 offset=0)
		[]byte{0x37, 0x03, 0x00},
	)
}

// i32ConstSeq — i32.const(支持 >127 的多字节 sleb128)。
func i32ConstSeq(v int) []byte {
	return concat([]byte{0x41}, sleb(int32(v)))
}

// --- wasm binary 构造 helper(同 p3indirect)---

func sec(id byte, payload []byte) []byte {
	return concat([]byte{id}, uleb(uint32(len(payload))), payload)
}

func importFuncEntry(mod, name string, typeIdx uint32) []byte {
	return concat(
		[]byte{byte(len(mod))}, []byte(mod),
		[]byte{byte(len(name))}, []byte(name),
		[]byte{0x00}, uleb(typeIdx),
	)
}

func importMemEntry(mod, name string) []byte {
	return concat(
		[]byte{byte(len(mod))}, []byte(mod),
		[]byte{byte(len(name))}, []byte(name),
		[]byte{0x02},       // kind=memory
		[]byte{0x00, 0x01}, // limits flags=0 min=1
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

func sleb(v int32) []byte {
	var out []byte
	for {
		c := byte(v & 0x7f)
		v >>= 7
		signBit := c&0x40 != 0
		if (v == 0 && !signBit) || (v == -1 && signBit) {
			out = append(out, c)
			return out
		}
		out = append(out, c|0x80)
	}
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
