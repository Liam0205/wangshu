package p3indirect

// envMemModule — 一个只导出 memory 的 "env" module 二进制(min=1 page,有 max
// 便于固定容量)。所有 gibbous module 实例 import 它,共见同一块 linear memory
// (= 主库 PW1 memadapter holder 的 spike 等价物)。
func envMemModule() []byte {
	var b []byte
	b = append(b, 0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00)
	// Memory section:1 个 memory,flags=1(有 max),min=1,max=16。
	b = append(b, sec(0x05, concat(
		uleb(1),
		[]byte{0x01, 0x01, 0x10}, // flags=1, min=1, max=16
	))...)
	// Export section:export "memory" = mem 0。
	b = append(b, sec(0x07, concat(
		uleb(1),
		[]byte{0x06}, []byte("memory"),
		[]byte{0x02, 0x00}, // kind=memory, index 0
	))...)
	return b
}
