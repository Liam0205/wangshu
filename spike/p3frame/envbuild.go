package p3frame

// env.memory holder module 生成(同 p3indirect membuild 的 holder 思路):
// 一个普通 module 导出一块 memory,spike module 与 host 共享之。

// buildEnvModule — 导出 env.memory(min=1 page = 64KiB,够放段 + 标志字)。
func buildEnvModule() []byte {
	var b []byte
	b = append(b, 0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00)
	// Memory section:1 个 memory,limits flags=0 min=1。
	b = append(b, sec(0x05, concat(uleb(1), []byte{0x00, 0x01}))...)
	// Export section:export "memory"。
	b = append(b, sec(0x07, concat(
		uleb(1), []byte{0x06}, []byte("memory"), []byte{0x02}, uleb(0),
	))...)
	return b
}
