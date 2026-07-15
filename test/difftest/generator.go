// Random script generator for differential fuzzing (12 §3.2).
//
// A controlled grammar generates deterministic scripts (same seed, same output):
// it only produces constructs that are "safe to diff" — numeric arithmetic /
// string concatenation / comparison / local variables / control flow / function
// calls. It avoids implementation-defined behavior (pairs order, tostring(table)
// address, etc.), guaranteeing the output is byte-for-byte diffable.
package difftest

import (
	"fmt"
	"math/rand"
	"strings"
)

type localKind uint8

const (
	localNum localKind = iota
	localStr
	localTbl
)

type genState struct {
	rng    *rand.Rand
	sb     strings.Builder
	locals []localKind
	depth  int
}

// generateScript produces a deterministic script ending with `return <expr>`.
func generateScript(seed int64) string {
	g := &genState{rng: rand.New(rand.NewSource(seed))}
	n := 3 + g.rng.Intn(5)
	for i := 0; i < n; i++ {
		g.stmt()
	}
	// Terminate: return the tostring concatenation of all locals (output depends on all paths).
	// A table local uses #t (length is diffable; content order is not).
	g.sb.WriteString("return ")
	if len(g.locals) == 0 {
		g.sb.WriteString(g.numExpr())
	} else {
		parts := make([]string, 0, len(g.locals))
		for i, k := range g.locals {
			if k == localTbl {
				parts = append(parts, fmt.Sprintf("tostring(#v%d)", i))
			} else {
				parts = append(parts, fmt.Sprintf("tostring(v%d)", i))
			}
		}
		g.sb.WriteString(strings.Join(parts, " .. \"|\" .. "))
	}
	g.sb.WriteString("\n")
	return g.sb.String()
}

func (g *genState) stmt() {
	switch g.rng.Intn(19) {
	case 0:
		g.declNum()
	case 1:
		g.declStr()
	case 2:
		g.forLoop()
	case 3:
		g.ifStmt()
	case 4:
		g.funcDef()
	case 5:
		g.declTbl()
	case 6:
		g.whileLoop()
	case 7:
		g.repeatLoop()
	case 8:
		g.closureDef()
	case 9:
		g.multiAssign()
	case 10:
		g.genericFor()
	case 11:
		g.nestedFunc()
	case 12:
		g.patternStmt()
	case 13:
		g.metaIndex()
	case 14:
		g.coroutineStmt()
	case 15:
		g.metaOperators()
	case 16:
		g.metaCallable()
	case 17:
		g.loadstringStmt()
	case 18:
		g.tostringMeta()
	}
}

func (g *genState) newLocal(k localKind) string {
	name := fmt.Sprintf("v%d", len(g.locals))
	g.locals = append(g.locals, k)
	return name
}

// pickLocal finds a local of the given type; creates a new one if none exists.
//
// The creation path must re-look-up by "the local index the declaration
// statement produced" (a decl* may cascade into creating locals of other types,
// so len-1 is not necessarily the target type).
func (g *genState) pickLocal(k localKind) string {
	var cand []int
	for i, lk := range g.locals {
		if lk == k {
			cand = append(cand, i)
		}
	}
	if len(cand) == 0 {
		switch k {
		case localNum:
			g.declNum()
		case localStr:
			g.declStr()
		case localTbl:
			g.declTbl()
		}
		// Rescan: take the first local of this type after creation
		for i, lk := range g.locals {
			if lk == k {
				return fmt.Sprintf("v%d", i)
			}
		}
	}
	return fmt.Sprintf("v%d", cand[g.rng.Intn(len(cand))])
}

func (g *genState) declNum() {
	name := g.newLocal(localNum)
	fmt.Fprintf(&g.sb, "local %s = %s\n", name, g.numExpr())
}

func (g *genState) declStr() {
	name := g.newLocal(localStr)
	fmt.Fprintf(&g.sb, "local %s = %s\n", name, g.strExpr())
}

// declTbl declares an array-table local + several write operations.
func (g *genState) declTbl() {
	name := g.newLocal(localTbl)
	n := 1 + g.rng.Intn(4)
	elems := make([]string, n)
	for i := range elems {
		elems[i] = g.numExpr()
	}
	fmt.Fprintf(&g.sb, "local %s = { %s }\n", name, strings.Join(elems, ", "))
	// Randomly append a write/read
	switch g.rng.Intn(3) {
	case 0:
		fmt.Fprintf(&g.sb, "%s[#%s + 1] = %s\n", name, name, g.numExpr())
	case 1:
		fmt.Fprintf(&g.sb, "%s[1] = %s\n", name, g.numExpr())
	case 2:
		acc := g.pickLocal(localNum)
		fmt.Fprintf(&g.sb, "%s = %s + (%s[1] or 0)\n", acc, acc, name)
	}
}

func (g *genState) forLoop() {
	acc := g.pickLocal(localNum)
	lo := 1 + g.rng.Intn(3)
	hi := lo + g.rng.Intn(10)
	step := ""
	if g.rng.Intn(3) == 0 {
		step = fmt.Sprintf(", %d", 1+g.rng.Intn(3))
	}
	fmt.Fprintf(&g.sb, "for i = %d, %d%s do %s = %s + i end\n", lo, hi, step, acc, acc)
}

func (g *genState) whileLoop() {
	cnt := g.newLocal(localNum)
	acc := g.pickLocal(localNum)
	limit := 1 + g.rng.Intn(8)
	fmt.Fprintf(&g.sb, "local %s = 0\nwhile %s < %d do %s = %s + 1; %s = %s + %s end\n",
		cnt, cnt, limit, cnt, cnt, acc, acc, cnt)
}

func (g *genState) repeatLoop() {
	cnt := g.newLocal(localNum)
	limit := 1 + g.rng.Intn(6)
	fmt.Fprintf(&g.sb, "local %s = 0\nrepeat %s = %s + 1 until %s >= %d\n",
		cnt, cnt, cnt, cnt, limit)
}

func (g *genState) ifStmt() {
	v := g.pickLocal(localNum)
	switch g.rng.Intn(3) {
	case 0:
		fmt.Fprintf(&g.sb, "if %s > %d then %s = %s else %s = %s end\n",
			v, g.rng.Intn(100), v, g.numExpr(), v, g.numExpr())
	case 1:
		// elseif chain
		fmt.Fprintf(&g.sb, "if %s > %d then %s = %s elseif %s > %d then %s = %s else %s = %s end\n",
			v, 50+g.rng.Intn(50), v, g.numExpr(),
			v, g.rng.Intn(50), v, g.numExpr(), v, g.numExpr())
	case 2:
		// logical-operator condition
		w := g.pickLocal(localNum)
		fmt.Fprintf(&g.sb, "if %s > 0 and %s >= 0 or %s == %s then %s = %s + 1 end\n",
			v, w, v, w, v, v)
	}
}

func (g *genState) funcDef() {
	name := g.newLocal(localNum)
	op := []string{"+", "-", "*"}[g.rng.Intn(3)]
	fmt.Fprintf(&g.sb, "local function f%s(a, b) return a %s b end\nlocal %s = f%s(%d, %d)\n",
		name, op, name, name, g.rng.Intn(100), 1+g.rng.Intn(100))
}

// closureDef: closure capture + multiple calls.
func (g *genState) closureDef() {
	name := g.newLocal(localNum)
	fmt.Fprintf(&g.sb, `local function mk%s()
  local n = %d
  return function() n = n + 1; return n end
end
local c%s = mk%s()
c%s()
local %s = c%s()
`, name, g.rng.Intn(10), name, name, name, name, name)
}

// multiAssign: multi-value return + truncate/pad with nil.
func (g *genState) multiAssign() {
	a := g.newLocal(localNum)
	b := g.newLocal(localNum)
	fmt.Fprintf(&g.sb, "local function two%s() return %s, %s end\nlocal %s, %s = two%s()\n",
		a, g.numExpr(), g.numExpr(), a, b, a)
}

// genericFor: generic for — ipairs order is deterministic and directly diffable;
// pairs uses order-independent aggregation (sum/count).
func (g *genState) genericFor() {
	t := g.pickLocal(localTbl)
	acc := g.pickLocal(localNum)
	if g.rng.Intn(2) == 0 {
		// ipairs: deterministic order
		fmt.Fprintf(&g.sb, "for i, x in ipairs(%s) do %s = %s + i * (x %% 97) end\n", t, acc, acc)
	} else {
		// pairs: only order-independent aggregation (key count + numeric sum)
		fmt.Fprintf(&g.sb, "for k, x in pairs(%s) do %s = %s + 1 + (type(x) == \"number\" and (x %% 89) or 0) end\n",
			t, acc, acc)
	}
}

// nestedFunc: two-level nested function definition (inner captures the outer parameter).
func (g *genState) nestedFunc() {
	name := g.newLocal(localNum)
	fmt.Fprintf(&g.sb, `local function outer%s(a)
  local function inner(b) return a + b end
  return inner(%s)
end
local %s = outer%s(%d)
`, name, g.numExpr(), name, name, g.rng.Intn(100))
}

// patternStmt: string.find/match/gsub over a controlled pattern set (fixed
// pattern whitelist, random subject).
func (g *genState) patternStmt() {
	name := g.newLocal(localStr)
	subjects := []string{
		`"abc 123 def"`, `"key=value;x=y"`, `"  spaced  "`, `"UPPER lower"`,
		`"a1b2c3"`, `"hello-world"`, `"[bracket]"`,
	}
	subj := subjects[g.rng.Intn(len(subjects))]
	switch g.rng.Intn(4) {
	case 0:
		pats := []string{`"%d+"`, `"%a+"`, `"%w+"`, `"[a-z]+"`}
		fmt.Fprintf(&g.sb, "local %s = tostring(string.match(%s, %s))\n",
			name, subj, pats[g.rng.Intn(len(pats))])
	case 1:
		pats := []string{`"%s"`, `"="`, `"%d"`}
		fmt.Fprintf(&g.sb, "local %s = tostring(string.find(%s, %s))\n",
			name, subj, pats[g.rng.Intn(len(pats))])
	case 2:
		fmt.Fprintf(&g.sb, "local %s = string.gsub(%s, \"%%a\", \"_\")\n", name, subj)
	case 3:
		fmt.Fprintf(&g.sb, `local %s = ""
for w in string.gmatch(%s, "%%w+") do %s = %s .. w .. "," end
`, name, subj, name, name)
	}
}

// metaIndex: metatable __index in two forms (table chain / function).
func (g *genState) metaIndex() {
	name := g.newLocal(localNum)
	if g.rng.Intn(2) == 0 {
		fmt.Fprintf(&g.sb, `local base%s = { v = %d }
local t%s = setmetatable({}, { __index = base%s })
local %s = t%s.v
`, name, g.rng.Intn(1000), name, name, name, name)
	} else {
		fmt.Fprintf(&g.sb, `local t%s = setmetatable({}, { __index = function(_, k) return #k + %d end })
local %s = t%s.somekey
`, name, g.rng.Intn(50), name, name)
	}
}

// coroutineStmt: coroutine yield-order diffing (yield order is deterministic).
func (g *genState) coroutineStmt() {
	name := g.newLocal(localStr)
	n := 2 + g.rng.Intn(3)
	fmt.Fprintf(&g.sb, `local co%s = coroutine.create(function(z)
  for i = 1, %d do z = coroutine.yield(z + i) end
  return z
end)
local %s = ""
local arg%s = %d
for i = 1, %d do
  local ok, v = coroutine.resume(co%s, arg%s)
  %s = %s .. tostring(v) .. ";"
end
`, name, n, name, name, g.rng.Intn(20), n+1, name, name, name, name)
}

// metaOperators: arithmetic/comparison metamethods (random one of
// __add/__lt/__le/__eq, deterministic behavior).
func (g *genState) metaOperators() {
	name := g.newLocal(localStr)
	switch g.rng.Intn(4) {
	case 0:
		// __add: b may be a bare number, must type-check before taking the value (a number is not indexable)
		fmt.Fprintf(&g.sb, `local mt%s = { __add = function(a, b)
  local av = type(a) == "table" and a.v or a
  local bv = type(b) == "table" and b.v or b
  return av + bv
end }
local o%s = setmetatable({ v = %d }, mt%s)
local %s = tostring(o%s + %d)
`, name, name, g.rng.Intn(50), name, name, name, g.rng.Intn(50))
	case 1:
		fmt.Fprintf(&g.sb, `local mt%s = { __lt = function(a, b) return a.v < b.v end }
local a%s = setmetatable({ v = %d }, mt%s)
local b%s = setmetatable({ v = %d }, mt%s)
local %s = tostring(a%s < b%s)
`, name, name, g.rng.Intn(50), name, name, g.rng.Intn(50), name, name, name, name)
	case 2:
		// __le falls back through __lt (5.1-specific)
		fmt.Fprintf(&g.sb, `local mt%s = { __lt = function(a, b) return a.v < b.v end }
local a%s = setmetatable({ v = %d }, mt%s)
local b%s = setmetatable({ v = %d }, mt%s)
local %s = tostring(a%s <= b%s)
`, name, name, g.rng.Intn(50), name, name, g.rng.Intn(50), name, name, name, name)
	case 3:
		fmt.Fprintf(&g.sb, `local mt%s = { __eq = function(a, b) return a.v == b.v end }
local a%s = setmetatable({ v = %d }, mt%s)
local b%s = setmetatable({ v = %d }, mt%s)
local %s = tostring(a%s == b%s)
`, name, name, g.rng.Intn(3), name, name, g.rng.Intn(3), name, name, name, name)
	}
}

// metaCallable: __call callable object.
func (g *genState) metaCallable() {
	name := g.newLocal(localNum)
	op := []string{"+", "*", "-"}[g.rng.Intn(3)]
	fmt.Fprintf(&g.sb, `local c%s = setmetatable({ base = %d }, { __call = function(self, x) return self.base %s x end })
local %s = c%s(%d)
`, name, g.rng.Intn(100), op, name, name, 1+g.rng.Intn(100))
}

// loadstringStmt: dynamic compile-and-run (the code string is deterministic).
func (g *genState) loadstringStmt() {
	name := g.newLocal(localNum)
	a, b := g.rng.Intn(100), 1+g.rng.Intn(100)
	op := []string{"+", "*", "-"}[g.rng.Intn(3)]
	fmt.Fprintf(&g.sb, "local f%s = loadstring(\"return %d %s %d\")\nlocal %s = f%s()\n",
		name, a, op, b, name, name)
}

// tostringMeta: __tostring metamethod.
func (g *genState) tostringMeta() {
	name := g.newLocal(localStr)
	fmt.Fprintf(&g.sb, `local o%s = setmetatable({}, { __tostring = function() return "obj<%d>" end })
local %s = tostring(o%s)
`, name, g.rng.Intn(1000), name, name)
}

// numExpr produces a safe numeric expression.
func (g *genState) numExpr() string {
	if g.depth > 3 {
		return fmt.Sprintf("%d", g.rng.Intn(1000))
	}
	g.depth++
	defer func() { g.depth-- }()
	switch g.rng.Intn(10) {
	case 0:
		return fmt.Sprintf("%d", g.rng.Intn(1000))
	case 1:
		// Control the number of decimal places to avoid %.14g edge cases (covered by dedicated cases in the seed corpus)
		return fmt.Sprintf("%.4f", g.rng.Float64()*100)
	case 2:
		return fmt.Sprintf("(%s + %s)", g.numExpr(), g.numExpr())
	case 3:
		return fmt.Sprintf("(%s * %s)", g.numExpr(), g.numExpr())
	case 4:
		return fmt.Sprintf("(%s - %s)", g.numExpr(), g.numExpr())
	case 5:
		return fmt.Sprintf("math.floor(%s)", g.numExpr())
	case 6:
		return fmt.Sprintf("math.max(%s, %s)", g.numExpr(), g.numExpr())
	case 7:
		return fmt.Sprintf("(%s %% %d)", g.numExpr(), 1+g.rng.Intn(9)) // nonzero mod
	case 8:
		return fmt.Sprintf("#%s", g.strExpr()) // string length
	case 9:
		// Short-circuit expression embedded in arithmetic: `(cond and K1 or K2) op K3` —
		// an eKNum with a jump chain cannot be folded (isnumeral); this shape once
		// slipped past every guard and triggered a Go panic.
		cond := []string{"true", "false", "(1 < 2)", "(2 < 1)"}[g.rng.Intn(4)]
		return fmt.Sprintf("((%s and %d or %d) + %d)",
			cond, g.rng.Intn(100), g.rng.Intn(100), g.rng.Intn(100))
	}
	return "0"
}

// strExpr produces a safe string expression.
func (g *genState) strExpr() string {
	words := []string{`"abc"`, `"hello"`, `"x"`, `""`, `"42"`}
	if g.depth > 2 {
		return words[g.rng.Intn(len(words))]
	}
	g.depth++
	defer func() { g.depth-- }()
	switch g.rng.Intn(7) {
	case 0, 1:
		return words[g.rng.Intn(len(words))]
	case 2:
		return fmt.Sprintf("(%s .. %s)", g.strExpr(), g.strExpr())
	case 3:
		return fmt.Sprintf("string.upper(%s)", g.strExpr())
	case 4:
		return fmt.Sprintf("string.sub(%s, %d)", g.strExpr(), 1+g.rng.Intn(3))
	case 5:
		return fmt.Sprintf("string.rep(%s, %d)", g.strExpr(), g.rng.Intn(4))
	case 6:
		return fmt.Sprintf("string.format(\"%%d\", %d)", g.rng.Intn(1000))
	}
	return `""`
}
