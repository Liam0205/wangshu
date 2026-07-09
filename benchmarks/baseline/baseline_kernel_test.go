// Shared kernel-shape sources for the baseline three-tier scripts.
//
// Tag-neutral (compiled in every build): the P3 build's promoted-path
// benches (baseline_gibbous_test.go) wrap these bodies via wrapKernel
// because the top-level chunk is vararg and never promotes (F1); the
// default build's _GopherKernel benches (baseline_test.go) run the SAME
// wrapped shape on gopher-lua so the README table's baseline P3 ratios
// compare like against like (issue #93: dividing a kernel×50 workload
// by a top-level×1 gopher number understated P3 by ~50x).
package baseline

// wrapKernel wraps a top-level script body into a non-vararg inner
// kernel called 50 times (a vararg top-level chunk never promotes, so
// benchmarking it directly would measure nothing; see the header note
// in baseline_gibbous_test.go).
func wrapKernel(body string) string {
	return "local function kernel()\n" + body + "\nend\nlocal t = 0\nfor _ = 1, 50 do t = kernel() end\nreturn t"
}

// Kernel bodies (semantically equal to baseline_test.go's top-level
// sources, minus the top-level shape).
const simpleBody = `
  local a, b = 1, 2
  local r = 0
  if a < b then r = a else r = b end
  return r`

const arithBody = `
  local x = 1.5
  return ((((x + 2) * x + 3) * x + 4) * x + 5) * x + 6`

const loopBody = `
  local s = 0
  for i = 1, 1000 do s = s + i * i end
  return s`
