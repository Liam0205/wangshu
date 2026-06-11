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

const luaPi = math.Pi

// luaHuge = +Inf(math.huge,5.1)。
func luaHuge() float64 { return math.Inf(1) }
