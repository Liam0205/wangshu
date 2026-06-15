package p3indirect

// S-C:跨 module call_indirect 经**共享 imported funcref 表**是否可行(R1 架构岔路)。
//
// S-B 证了「重编整个 module + 新实例」可行(Arch-1 rebuild-all),但复杂(代际实例 +
// 跨代分派守卫)。Arch-2 更简单:保留每 Proto 一 module,所有 module 共享 env 导出的
// 一张 funcref 表——新升层 = 实例化一个新 module,它经 active element 段把自己的函数
// **自注册**进表的一个槽,免重编已有 module。caller module `call_indirect` 该表槽
// 跨 module 调到 provider 的函数。
//
// 生死问题:wazero 是否支持「module B 的 active element 段填 env 导出的 imported 表 +
// module C 经 imported 表 call_indirect 解析到 module B 定义的函数」。通过 ⇒ Arch-2
// (R1 大幅简化);失败 ⇒ 退 Arch-1(已证)。

// envTableMemModule — env module 导出 memory + 一张 funcref table(min=numSlots)。
// 所有 gibbous module import 这两者;表是它们共享的「升层函数注册表」。
func envTableMemModule(numSlots int) []byte {
	var b []byte
	b = append(b, 0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00)
	// Table section:1 张 funcref 表,flags=0 min=numSlots。
	b = append(b, sec(0x04, concat(
		uleb(1),
		[]byte{0x70},           // elemtype funcref
		[]byte{0x00},           // limits flags=0(no max)
		uleb(uint32(numSlots)), // min
	))...)
	// Memory section:1 memory min=1 max=16。
	b = append(b, sec(0x05, concat(
		uleb(1),
		[]byte{0x01, 0x01, 0x10},
	))...)
	// Export section:table(kind=0x01 idx0)+ memory(kind=0x02 idx0)。
	b = append(b, sec(0x07, concat(
		uleb(2),
		[]byte{0x05}, []byte("table"), []byte{0x01, 0x00},
		[]byte{0x06}, []byte("memory"), []byte{0x02, 0x00},
	))...)
	return b
}

// providerModule — 一个「升层 Proto」module:import env.table + env.memory,定义
// leaf(x)=x*mulK+1,经 active element 段把 leaf 自注册进 table[slot]。无需 export
// (实例化即运行 element 段填表)。
//
// func 索引空间:table/memory import 不占 func index ⟹ leaf = func 0。
func providerModule(slot int, mulK byte) []byte {
	var b []byte
	b = append(b, 0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00)
	// Type section:type0 (i32)->(i32)。
	b = append(b, sec(0x01, concat(
		uleb(1),
		[]byte{0x60, 0x01, 0x7f, 0x01, 0x7f},
	))...)
	// Import section:env.table + env.memory。
	b = append(b, sec(0x02, concat(
		uleb(2),
		importTableEntry("env", "table", 0),
		importMemEntry("env", "memory"),
	))...)
	// Function section:1 func(leaf)type0。
	b = append(b, sec(0x03, concat(uleb(1), uleb(0)))...)
	// Element section:active segment,table 0,offset=(i32.const slot),func[0]=leaf。
	b = append(b, sec(0x09, concat(
		uleb(1),                                       // 1 segment
		[]byte{0x00},                                  // flags=0(active, table 0, offset expr)
		[]byte{0x41}, sleb(int32(slot)), []byte{0x0b}, // offset = (i32.const slot) end
		uleb(1), // 1 funcidx
		uleb(0), // leaf = func 0
	))...)
	// Code section:leaf(x)=x*mulK+1。
	leaf := concat(
		[]byte{0x00}, // 0 locals
		[]byte{0x20, 0x00, 0x41, mulK, 0x6c, 0x41, 0x01, 0x6a}, // local.get0; const mulK; mul; const1; add
		[]byte{0x0b},
	)
	b = append(b, sec(0x0a, concat(uleb(1), uleb(uint32(len(leaf))), leaf))...)
	return b
}

// callerModule — driver(n) 经 imported 表 call_indirect 槽 slot 调 leaf 累加 n 次。
// import env.table + env.memory;driver = func 0(table/memory import 不占 func idx)。
func callerModule(slot int) []byte {
	var b []byte
	b = append(b, 0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00)
	b = append(b, sec(0x01, concat(
		uleb(1),
		[]byte{0x60, 0x01, 0x7f, 0x01, 0x7f}, // type0 (i32)->(i32)
	))...)
	b = append(b, sec(0x02, concat(
		uleb(2),
		importTableEntry("env", "table", 0),
		importMemEntry("env", "memory"),
	))...)
	b = append(b, sec(0x03, concat(uleb(1), uleb(0)))...) // driver func0 type0
	b = append(b, sec(0x07, concat(
		uleb(1),
		[]byte{0x06}, []byte("driver"), []byte{0x00, 0x00}, // export driver func0
	))...)
	// driver body:locals $acc;loop 调 call_indirect table[slot] 累加。
	locals := []byte{0x01, 0x01, 0x7f}
	body := concat(
		[]byte{
			0x02, 0x40, // block $exit
			0x03, 0x40, // loop $top
			0x20, 0x00, // local.get $n
			0x45,       // i32.eqz
			0x0d, 0x01, // br_if $exit
			0x20, 0x01, // local.get $acc
			0x20, 0x00, // local.get $n (arg)
			0x41}, sleb(int32(slot)), // i32.const slot (table index operand)
		[]byte{0x11, 0x00, 0x00}, // call_indirect type0 table0
		[]byte{
			0x6a,       // i32.add
			0x21, 0x01, // local.set $acc
			0x20, 0x00, 0x41, 0x01, 0x6b, 0x21, 0x00, // n--
			0x0c, 0x00, // br $top
			0x0b,       // end loop
			0x0b,       // end block
			0x20, 0x01, // local.get $acc
		},
	)
	driver := concat(locals, body, []byte{0x0b})
	b = append(b, sec(0x0a, concat(uleb(1), uleb(uint32(len(driver))), driver))...)
	return b
}

// importTableEntry — import mod.name : table(funcref, flags=0 min)。
func importTableEntry(mod, name string, min uint32) []byte {
	return concat(
		[]byte{byte(len(mod))}, []byte(mod),
		[]byte{byte(len(name))}, []byte(name),
		[]byte{0x01}, // kind=table
		[]byte{0x70}, // elemtype funcref
		[]byte{0x00}, // limits flags=0
		uleb(min),    // min
	)
}

// sleb — signed LEB128(i32.const 立即数)。slot 小(<64)单字节即可,但通用编码。
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
