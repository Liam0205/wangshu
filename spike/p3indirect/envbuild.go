package p3indirect

// envMemModule — an "env" module binary that exports only memory (min=1 page,
// with a max for a fixed capacity). All gibbous module instances import it and
// share the same linear memory (= the spike equivalent of the main library's
// PW1 memadapter holder).
func envMemModule() []byte {
	var b []byte
	b = append(b, 0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00)
	// Memory section: 1 memory, flags=1 (has max), min=1, max=16.
	b = append(b, sec(0x05, concat(
		uleb(1),
		[]byte{0x01, 0x01, 0x10}, // flags=1, min=1, max=16
	))...)
	// Export section: export "memory" = mem 0.
	b = append(b, sec(0x07, concat(
		uleb(1),
		[]byte{0x06}, []byte("memory"),
		[]byte{0x02, 0x00}, // kind=memory, index 0
	))...)
	return b
}
