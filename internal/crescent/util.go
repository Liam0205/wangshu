// Helpers — sprintf wrapper (avoids depending on the fmt package directly,
// centralizing the error-formatting entry point).
package crescent

import "fmt"

func sprintf(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}

// roundUpPow2 rounds n up to the nearest power of two (0 → 0). AllocTable
// requires hsize to be a power of two (the panic check in table.go AllocTable).
func roundUpPow2(n uint32) uint32 {
	if n == 0 {
		return 0
	}
	if n&(n-1) == 0 {
		return n
	}
	v := uint32(1)
	for v < n {
		v <<= 1
	}
	return v
}
