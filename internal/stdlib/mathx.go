// math 的随机数与浮点 helper(独立文件,集中 math 依赖)。
package stdlib

import (
	"math"
	"math/rand"
	"sync"
)

var (
	rngMu sync.Mutex
	rng   = rand.New(rand.NewSource(0)) // Lua 5.1 默认种子确定
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
	ip, fp := math.Modf(x)
	return ip, fp
}
func atan(x float64) float64  { return math.Atan(x) }
func asin(x float64) float64  { return math.Asin(x) }
func acos(x float64) float64  { return math.Acos(x) }
func deg(x float64) float64   { return x * 180 / math.Pi }
func rad(x float64) float64   { return x * math.Pi / 180 }
func log10(x float64) float64 { return math.Log10(x) }

func atan2(y, x float64) float64 { return math.Atan2(y, x) }
func sinh(x float64) float64     { return math.Sinh(x) }
func cosh(x float64) float64     { return math.Cosh(x) }
func tanh(x float64) float64     { return math.Tanh(x) }
func frexp(x float64) (float64, int) {
	f := math.Abs(x)
	if f == 0 || math.IsInf(f, 0) || math.IsNaN(f) {
		return x, 0
	}
	exp := int(math.Floor(math.Log2(f))) + 1
	m := x / math.Ldexp(1, exp)
	return m, exp
}
func ldexp(m float64, e int) float64 { return math.Ldexp(m, e) }

const luaPi = math.Pi

// luaHuge = +Inf(math.huge,5.1)。
func luaHuge() float64 { return math.Inf(1) }
