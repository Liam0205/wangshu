//go:build wangshu_p3

package memadapter

// buildMemoryHolderModuleBinary 构造一个声明 + 导出 memory 与一张 funcref
// table 的 wasm module 二进制(手写,不引入 wat2wasm 构建期工具,同
// spike/p3boundary 经验)。
//
// 等价 WAT:
//
//	(module
//	  (memory (export "memory") <initPage> <maxPage>)
//	  (table (export "table") <tableSlots> funcref))
//
// **table(PW10 Arch-2)**:gibbous module 间共享的「升层函数注册表」——每个
// 升层 Proto 的 module 经 active element 段把自己的 `run` 自注册进一个 slot,
// gibbous→gibbous CALL 经 `call_indirect <slot>` 跨 module 直达,免 Go 往返
// (spike/p3indirect S-C 验证)。表在此声明一次,容量 = tableSlots(slot 上限)。
//
// Wasm binary 格式:magic(4)+version(4)+Table(4)+Memory(5)+Export(7)。
func buildMemoryHolderModuleBinary(initPage, maxPage, tableSlots uint32) []byte {
	var b []byte
	// magic + version
	b = append(b, 0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00)

	// Table section (id=4):1 张 funcref 表,limits flags=0(无 max),min=tableSlots。
	// 固定容量(无 max):active element 段写 table[slot] 要求 min ≥ slot+1,故预留足量。
	tablePayload := []byte{0x01, 0x70, 0x00} // count=1, elemtype funcref(0x70), flags=0(no max)
	tablePayload = append(tablePayload, uleb(tableSlots)...)
	b = append(b, section(0x04, tablePayload)...)

	// Memory section (id=5):1 个 memory,limits flags=1(有 max),min, max
	memPayload := []byte{0x01, 0x01} // count=1, flags=1(min+max)
	memPayload = append(memPayload, uleb(initPage)...)
	memPayload = append(memPayload, uleb(maxPage)...)
	b = append(b, section(0x05, memPayload)...)

	// Export section (id=7):export "memory" = mem 0;"table" = table 0
	expPayload := []byte{0x02} // count=2
	expPayload = append(expPayload, byte(len("memory")))
	expPayload = append(expPayload, []byte("memory")...)
	expPayload = append(expPayload, 0x02, 0x00) // kind=mem(2), index=0
	expPayload = append(expPayload, byte(len("table")))
	expPayload = append(expPayload, []byte("table")...)
	expPayload = append(expPayload, 0x01, 0x00) // kind=table(1), index=0
	b = append(b, section(0x07, expPayload)...)

	return b
}

// section 包裹一个 wasm section:id + uleb(len) + payload。
func section(id byte, payload []byte) []byte {
	out := []byte{id}
	out = append(out, uleb(uint32(len(payload)))...)
	return append(out, payload...)
}

// uleb 编码 unsigned LEB128。
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
