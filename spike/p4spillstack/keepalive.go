//go:build linux && amd64

package p4spillstack

import "runtime"

// keepAlive prevents the GC from reclaiming x until this point. Used to
// hold the spill-stack backing alive while a segment runs on it (the
// trampoline holds only a raw uintptr into the buffer, which the GC does
// not treat as a reference).
func keepAlive(x any) {
	runtime.KeepAlive(x)
}
