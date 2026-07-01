//go:build wangshu_p4 && !(linux && amd64)

package peroptranslator

// saveGoG stub for non-linux/amd64 platforms (no PJ10 native emit yet).
func saveGoG(_ *uintptr) {}
