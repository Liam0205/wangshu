package p3frame

// env.memory holder module generation (same holder idea as p3indirect's
// membuild): an ordinary module exports a block of memory, shared between the
// spike module and the host.

// buildEnvModule — exports env.memory (min=1 page = 64KiB, enough for the segment + flag words).
func buildEnvModule() []byte {
	var b []byte
	b = append(b, 0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00)
	// Memory section: 1 memory, limits flags=0 min=1.
	b = append(b, sec(0x05, concat(uleb(1), []byte{0x00, 0x01}))...)
	// Export section: export "memory".
	b = append(b, sec(0x07, concat(
		uleb(1), []byte{0x06}, []byte("memory"), []byte{0x02}, uleb(0),
	))...)
	return b
}
