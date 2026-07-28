// Random-number and floating-point helpers for math (separate file, centralizes the math dependency).
package stdlib

import (
	"math"
	"math/rand"
	"sync"
)

var (
	rngMu sync.Mutex
	rng   = rand.New(rand.NewSource(0)) // Lua 5.1 uses a fixed default seed
)

func rngFloat() float64 {
	rngMu.Lock()
	defer rngMu.Unlock()
	return rng.Float64()
}

func rngInt(lo, hi int64) int64 {
	rngMu.Lock()
	defer rngMu.Unlock()
	return lo + rng.Int63n(hi-lo+1)
}

func rngSeed(seed int64) {
	rngMu.Lock()
	defer rngMu.Unlock()
	rng = rand.New(rand.NewSource(seed))
}

func pow(a, b float64) float64  { return math.Pow(a, b) }
func fmod(a, b float64) float64 { return math.Mod(a, b) }
func modf(x float64) (float64, float64) {
	// C modf gives an infinity an integral part of the same infinity and a
	// fractional part of ZERO (signed like x). Go's math.Modf returns NaN for the
	// fraction, so math.modf(1/0) was "inf nan" against C's "inf 0".
	if math.IsInf(x, 0) {
		return x, math.Copysign(0, x)
	}
	ip, fp := math.Modf(x)
	return ip, fp
}
func atan(x float64) float64 { return math.Atan(x) }
func asin(x float64) float64 { return math.Asin(x) }
func acos(x float64) float64 { return math.Acos(x) }
func deg(x float64) float64  { return x * 180 / math.Pi }

// rad multiplies by the precomputed radians-per-degree constant rather than
// computing x*pi/180, which overflows to +Inf for x near DBL_MAX even though the
// result is representable: math.rad(1.797e308) is 3.1375664143846e+306 in C and
// was inf here. PUC uses the same single constant (RADIANS_PER_DEGREE).
func rad(x float64) float64   { return x * (math.Pi / 180) }
func log10(x float64) float64 { return math.Log10(x) }

func atan2(y, x float64) float64 { return math.Atan2(y, x) }
func sinh(x float64) float64     { return math.Sinh(x) }
func cosh(x float64) float64     { return math.Cosh(x) }
func tanh(x float64) float64     { return math.Tanh(x) }
func frexp(x float64) (float64, int) {
	// Delegate to math.Frexp rather than deriving the exponent from Log2: the
	// hand-rolled version was off by one near DBL_MAX (reporting 1025 where C
	// reports 1024), because Log2 of a value that close to the top rounds up and
	// the subsequent Ldexp cannot represent the divisor. math.Frexp is exact and
	// already returns C's convention (zero, inf and NaN yield x with exp 0).
	return math.Frexp(x)
}
func ldexp(m float64, e int) float64 { return math.Ldexp(m, e) }

const luaPi = math.Pi

// luaHuge = +Inf (math.huge, 5.1).
func luaHuge() float64 { return math.Inf(1) }
