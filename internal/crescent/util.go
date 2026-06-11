// Helpers — sprintf wrapper(避免直接依赖 fmt 包,集中错误格式入口)。
package crescent

import "fmt"

func sprintf(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}

// roundUpPow2 把 n 向上对齐到最接近的 2 的幂(0 → 0)。AllocTable 要求
// hsize 是 2 的幂(table.go AllocTable 的 panic 校验)。
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
