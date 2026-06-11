// Helpers — sprintf wrapper(避免直接依赖 fmt 包,集中错误格式入口)。
package crescent

import "fmt"

func sprintf(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}
