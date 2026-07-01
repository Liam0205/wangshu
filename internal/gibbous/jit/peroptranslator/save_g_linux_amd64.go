//go:build wangshu_p4 && linux && amd64

package peroptranslator

// saveGoG writes the current Go G (R14 on amd64 ABIInternal) into *slot.
// See save_g_amd64.s for detail. This is called from ordinary Go code
// just before entering a mmap segment that plans to call Go helpers,
// so the mmap emit can restore R14 = G before each helper call.
//
//go:noescape
func saveGoG(slot *uintptr)
