// OpCode 完整表(02 §4)。编号即枚举(iota 顺序),38..63 预留给 P2 profile / P3 tier guard。
package bytecode

// OpCode is a 6-bit opcode identifier.
type OpCode uint8

// Opcodes 0..37 are P1 active; 38..63 are reserved for P2/P3 (02 §4 末注)。
const (
	MOVE      OpCode = 0  // R(A) := R(B)
	LOADK     OpCode = 1  // R(A) := K(Bx)
	LOADBOOL  OpCode = 2  // R(A) := bool(B); if C≠0 then pc++
	LOADNIL   OpCode = 3  // R(A..B) := nil  (闭区间)
	GETUPVAL  OpCode = 4  // R(A) := Upval(B)
	SETUPVAL  OpCode = 5  // Upval(B) := R(A)
	GETGLOBAL OpCode = 6  // R(A) := Gtable[K(Bx)]                            ** IC
	SETGLOBAL OpCode = 7  // Gtable[K(Bx)] := R(A)                            ** IC
	GETTABLE  OpCode = 8  // R(A) := R(B)[RK(C)]   (__index)                  ** IC
	SETTABLE  OpCode = 9  // R(A)[RK(B)] := RK(C)  (__newindex)               ** IC
	NEWTABLE  OpCode = 10 // R(A) := {}, 预分配 array B / hash C(int2fb)
	SELF      OpCode = 11 // R(A+1) := R(B); R(A) := R(B)[RK(C)]              ** IC
	ADD       OpCode = 12 // R(A) := RK(B) + RK(C)   (__add)                  ** IC
	SUB       OpCode = 13 // R(A) := RK(B) - RK(C)   (__sub)
	MUL       OpCode = 14 // R(A) := RK(B) * RK(C)   (__mul)
	DIV       OpCode = 15 // R(A) := RK(B) / RK(C)   (__div, /0=±Inf)
	MOD       OpCode = 16 // R(A) := RK(B) % RK(C)   Lua: a-floor(a/b)*b
	POW       OpCode = 17 // R(A) := RK(B) ^ RK(C)   math.pow
	UNM       OpCode = 18 // R(A) := -R(B)           (__unm)
	NOT       OpCode = 19 // R(A) := not R(B)        (无 metamethod)
	LEN       OpCode = 20 // R(A) := #R(B)           (string len / table border / __len*)
	CONCAT    OpCode = 21 // R(A) := R(B) .. ... .. R(C)   (右结合;__concat)
	JMP       OpCode = 22 // pc += sBx                (无条件跳转)
	EQ        OpCode = 23 // if (RK(B)==RK(C)) ≠ A then pc++  (__eq)
	LT        OpCode = 24 // if (RK(B)<RK(C)) ≠ A then pc++   (__lt)
	LE        OpCode = 25 // if (RK(B)<=RK(C)) ≠ A then pc++  (__le;5.1 回退到 __lt)
	TEST      OpCode = 26 // if Truthy(R(A)) ≠ C then pc++
	TESTSET   OpCode = 27 // if Truthy(R(B))==C then R(A):=R(B) else pc++
	CALL      OpCode = 28 // R(A)(R(A+1..A+B-1)),返回回填 R(A..A+C-2);B/C=0 见 §3
	TAILCALL  OpCode = 29 // 尾调用,复用当前帧
	RETURN    OpCode = 30 // 返回 R(A..A+B-2);B=0 到 top
	FORLOOP   OpCode = 31 // 数值 for 回边(热点采样点)
	FORPREP   OpCode = 32 // 数值 for 准备
	TFORLOOP  OpCode = 33 // 泛型 for(__call iter)
	SETLIST   OpCode = 34 // 表构造批量填数组(FPF=50)
	CLOSE     OpCode = 35 // 关闭所有 ≥ R(A) 的开放 upvalue
	CLOSURE   OpCode = 36 // R(A) := closure(Proto[Bx]) + 后随 nupvals 条伪指令
	VARARG    OpCode = 37 // R(A..A+B-2) := ...   B=0 到 top

	// numOps 当前活跃 opcode 数,02 §4 注:38..63 预留给 P2/P3。
	numOps = int(VARARG) + 1
)

// String returns a human-readable opcode name (用于反汇编与诊断,不进热路径)。
func (op OpCode) String() string {
	if int(op) >= numOps {
		return "INVALID"
	}
	return opcodeNames[op]
}

var opcodeNames = [...]string{
	"MOVE", "LOADK", "LOADBOOL", "LOADNIL", "GETUPVAL", "SETUPVAL",
	"GETGLOBAL", "SETGLOBAL", "GETTABLE", "SETTABLE", "NEWTABLE", "SELF",
	"ADD", "SUB", "MUL", "DIV", "MOD", "POW",
	"UNM", "NOT", "LEN", "CONCAT",
	"JMP", "EQ", "LT", "LE", "TEST", "TESTSET",
	"CALL", "TAILCALL", "RETURN",
	"FORLOOP", "FORPREP", "TFORLOOP", "SETLIST",
	"CLOSE", "CLOSURE", "VARARG",
}

// Format 描述一条 opcode 用哪种编码。
type Format uint8

const (
	FmtABC  Format = 0
	FmtABx  Format = 1
	FmtAsBx Format = 2
)

// FormatOf returns the encoding format used by op.
func FormatOf(op OpCode) Format {
	switch op {
	case LOADK, GETGLOBAL, SETGLOBAL, CLOSURE:
		return FmtABx
	case JMP, FORLOOP, FORPREP:
		return FmtAsBx
	default:
		return FmtABC
	}
}
