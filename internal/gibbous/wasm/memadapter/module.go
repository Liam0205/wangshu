//go:build wangshu_p3

package memadapter

// buildMemoryHolderModuleBinary 构造一个仅声明 + 导出 memory 的 wasm module
// 二进制(手写,不引入 wat2wasm 构建期工具,同 spike/p3boundary 经验)。
//
// 等价 WAT:
//
//	(module
//	  (memory (export "memory") <initPage> <maxPage>))
//
// Wasm binary 格式:magic(4) + version(4) + Memory section(5) + Export section(7)。
func buildMemoryHolderModuleBinary(initPage, maxPage uint32) []byte {
	var b []byte
	// magic + version
	b = append(b, 0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00)

	// Memory section (id=5):1 个 memory,limits flags=1(有 max),min, max
	memPayload := []byte{0x01, 0x01} // count=1, flags=1(min+max)
	memPayload = append(memPayload, uleb(initPage)...)
	memPayload = append(memPayload, uleb(maxPage)...)
	b = append(b, section(0x05, memPayload)...)

	// Export section (id=7):export "memory" = mem index 0
	expPayload := []byte{0x01} // count=1
	expPayload = append(expPayload, byte(len("memory")))
	expPayload = append(expPayload, []byte("memory")...)
	expPayload = append(expPayload, 0x02, 0x00) // kind=mem(2), index=0
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
