//go:build wangshu_p3

package wasm

// gibbous module 二进制组装(02-translation §7 wazero 适配)。
//
// 把翻译产物(function body 字节,translate.go)包成一个完整的 Wasm module
// 二进制:import memory(共享 arena 收养的那块)+ import helpers(env.h_*)+
// 一个导出的入口函数 proto_entry。
//
// module 结构(section 顺序按 Wasm spec):
//   Type(1)     :函数签名(入口 + 各 helper)
//   Import(2)   :env.memory + env.h_getupval/h_setupval/h_return/h_safepoint
//   Function(3) :本地函数(入口)的 type 索引
//   Export(7)   :导出入口函数 "run"
//   Code(10)    :入口函数体

// helper 签名(对应 helpers_index.go 的 import 顺序)。
// 每个 helper 一个 type;入口函数也一个 type。
//
// type 索引布局:
//   type 0: (i32, i32) -> (i64)            h_getupval
//   type 1: (i32, i32, i64) -> ()          h_setupval
//   type 2: (i32, i32, i32, i32) -> (i32)  h_return
//   type 3: (i32, i32) -> ()               h_safepoint
//   type 4: (i32) -> (i32)                 入口 run(base) -> status
//   type 5: (i32,i32,i32,i32,i32,i32)->(i32) h_arith(base,pc,op,b,c,a)
//   type 6: (i32,i32,i32,i32) -> (i32)     h_unm(base,pc,b,a) / h_eq(base,pc,b,c)
//   type 7: (i32,i32,i32,i32) -> (i32)     h_len(base,pc,b,a)
//   type 8: (i32,i32,i32,i32,i32) -> (i32) h_concat(base,pc,a,b,c) / h_compare(base,pc,op,b,c)
//   type 9: (i32,i32,i32) -> (i32)         h_forprep(base,pc,a)

const (
	typeGetUpval  = 0
	typeSetUpval  = 1
	typeReturn    = 2
	typeSafepoint = 3
	typeEntry     = 4
	typeArith     = 5
	typeUnm       = 6 // 同 typeReturn 形状(i32×4→i32)但单列以便阅读
	typeLen       = 7
	typeConcat    = 8
	typeForPrep   = 9
	typeCall      = 10 // (i32×5)->(i64)  h_call 返回新 base / 负哨兵
	typeTForLoop  = 11 // (i32×4)->(i64)  h_tforloop 返回新 base / -1 ERR / -2 退出
	typeCallErr   = 12 // ()->()  h_callerr:call_indirect 直调失败补弹遗留 gibbous 帧(PW10 R3)
	numTypes      = 13
)

// Wasm value type 编码。
const (
	wvtI32 byte = 0x7f
	wvtI64 byte = 0x7e
	wvtF64 byte = 0x7c
)

// buildGibbousModuleBinary 组装完整 module 二进制。
//
// body 是 translate 产出的入口函数体(不含 local decl 与末尾 end)。
// slot 是本 module 的 run 在共享 env.table 里的槽号(PW10 Arch-2):Element 段
// active 写 table[slot] = run,使 gibbous→gibbous 可经 call_indirect 跨 module 直达。
// slot == maxTableSlots(表满哨兵)时不发 Element 段(避免越界写),该 module 仍可
// 编译执行,只是不进表(gibbous→它 回退 h_call)。
func buildGibbousModuleBinary(body []byte, slot uint32) []byte {
	var b []byte
	b = append(b, 0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00) // magic+version

	b = append(b, typeSection()...)
	b = append(b, importSection()...)
	b = append(b, functionSection()...)
	b = append(b, exportSection()...)
	if slot < maxTableSlots {
		b = append(b, elementSection(slot)...)
	}
	b = append(b, codeSectionEntry(body)...)
	return b
}

// elementSection 把入口 run(function index = numHelpers,import 后第一个本地函数)
// active 注册进共享 env.table 的 slot(PW10 Arch-2 自注册)。
//
//	(elem (i32.const slot) func $run)
//
// flags=0:active,table 0(唯一 import 表),offset 由 const expr 给。
func elementSection(slot uint32) []byte {
	var p []byte
	p = append(p, uleb32(1)...)                // 1 个 segment
	p = append(p, 0x00)                        // flags=0(active, table 0, offset expr)
	p = append(p, 0x41)                        // i32.const
	p = append(p, sleb32Bytes(int32(slot))...) // offset = slot
	p = append(p, 0x0b)                        // end(offset expr 结束)
	p = append(p, uleb32(1)...)                // 1 个 funcidx
	p = append(p, uleb32(numHelpers)...)       // run = import 后第一个本地 func
	return sectionOf(0x09, p)
}

// sleb32Bytes 有符号 LEB128(i32.const 立即数;slot 是非负小数,但走通用编码)。
func sleb32Bytes(v int32) []byte {
	var out []byte
	for {
		c := byte(v & 0x7f)
		v >>= 7
		signBit := c&0x40 != 0
		if (v == 0 && !signBit) || (v == -1 && signBit) {
			out = append(out, c)
			return out
		}
		out = append(out, c|0x80)
	}
}

// typeSection 声明 5 个函数类型。
func typeSection() []byte {
	var p []byte
	p = append(p, uleb32(numTypes)...) // count

	// type 0: (i32,i32)->(i64)
	p = append(p, 0x60, 0x02, wvtI32, wvtI32, 0x01, wvtI64)
	// type 1: (i32,i32,i64)->()
	p = append(p, 0x60, 0x03, wvtI32, wvtI32, wvtI64, 0x00)
	// type 2: (i32,i32,i32,i32)->(i32)
	p = append(p, 0x60, 0x04, wvtI32, wvtI32, wvtI32, wvtI32, 0x01, wvtI32)
	// type 3: (i32,i32)->()
	p = append(p, 0x60, 0x02, wvtI32, wvtI32, 0x00)
	// type 4: (i32)->(i32)
	p = append(p, 0x60, 0x01, wvtI32, 0x01, wvtI32)
	// type 5: (i32,i32,i32,i32,i32,i32)->(i32)  h_arith
	p = append(p, 0x60, 0x06, wvtI32, wvtI32, wvtI32, wvtI32, wvtI32, wvtI32, 0x01, wvtI32)
	// type 6: (i32,i32,i32,i32)->(i32)  h_unm
	p = append(p, 0x60, 0x04, wvtI32, wvtI32, wvtI32, wvtI32, 0x01, wvtI32)
	// type 7: (i32,i32,i32,i32)->(i32)  h_len
	p = append(p, 0x60, 0x04, wvtI32, wvtI32, wvtI32, wvtI32, 0x01, wvtI32)
	// type 8: (i32,i32,i32,i32,i32)->(i32)  h_concat / h_compare
	p = append(p, 0x60, 0x05, wvtI32, wvtI32, wvtI32, wvtI32, wvtI32, 0x01, wvtI32)
	// type 9: (i32,i32,i32)->(i32)  h_forprep
	p = append(p, 0x60, 0x03, wvtI32, wvtI32, wvtI32, 0x01, wvtI32)
	// type 10: (i32,i32,i32,i32,i32)->(i64)  h_call(base,pc,a,b,c → newbase/-1)
	p = append(p, 0x60, 0x05, wvtI32, wvtI32, wvtI32, wvtI32, wvtI32, 0x01, wvtI64)
	// type 11: (i32,i32,i32,i32)->(i64)  h_tforloop(base,pc,a,c → newbase/-1/-2)
	p = append(p, 0x60, 0x04, wvtI32, wvtI32, wvtI32, wvtI32, 0x01, wvtI64)
	// type 12: ()->()  h_callerr(PW10 R3:无参无返,补弹遗留 gibbous 帧)
	p = append(p, 0x60, 0x00, 0x00)

	return sectionOf(0x01, p)
}

// importSection 声明 import memory + 4 个 helper。
//
// memory 从 "env" module(memadapter holder,普通 module export memory);
// helper 从 "host" module(wazero HostModuleBuilder 注册的 Go 函数)。
// 两个 module name 不同——wazero HostModuleBuilder 不能 export memory,
// 普通 module 不能注册 Go host 函数,故 memory 与 helper 分属不同 module
// (PW2-c 跨 module memory 共享已 spike 验证)。
//
// import 顺序决定 helper 的 function index(helpers_index.go 常量):
// h_getupval=0, h_setupval=1, h_return=2, h_safepoint=3。memory import 不占
// function index 空间。
func importSection() []byte {
	var p []byte
	// count = 1 memory + 1 table + 24 funcs = 26(PW10 R3 加 env.h_callerr)
	p = append(p, uleb32(26)...)

	// import env.memory : memory(limits flags=0 min=1)——共享 holder 的 memory
	p = append(p, importEntry("env", "memory", 0x02, []byte{0x00, 0x01})...)

	// import env.table : funcref table(PW10 Arch-2)——共享 holder 那张升层函数
	// 注册表。本 module 的 run 经 element 段自注册进一个 slot;gibbous→gibbous
	// CALL 经 call_indirect 此表跨 module 直达被调 run(R3 接线)。import 表不占
	// function index 空间(同 memory),故 helper / entry func index 不变。
	// limits flags=0 min=0(import 声明「至少 0」,env 实供 TableSlots)。
	p = append(p, importEntry("env", "table", 0x01, []byte{0x70, 0x00, 0x00})...)

	// import host.h_* : func(顺序 = function index = helpers_index.go 常量)
	p = append(p, importFuncEntry("host", "h_getupval", typeGetUpval)...)
	p = append(p, importFuncEntry("host", "h_setupval", typeSetUpval)...)
	p = append(p, importFuncEntry("host", "h_return", typeReturn)...)
	p = append(p, importFuncEntry("host", "h_safepoint", typeSafepoint)...)
	p = append(p, importFuncEntry("host", "h_arith", typeArith)...)
	p = append(p, importFuncEntry("host", "h_unm", typeUnm)...)
	p = append(p, importFuncEntry("host", "h_len", typeLen)...)
	p = append(p, importFuncEntry("host", "h_concat", typeConcat)...)
	p = append(p, importFuncEntry("host", "h_compare", typeConcat)...) // (i32×5→i32)
	p = append(p, importFuncEntry("host", "h_eq", typeUnm)...)         // (i32×4→i32)
	p = append(p, importFuncEntry("host", "h_forprep", typeForPrep)...)
	// PW5 表 IC 助手:gettable/settable/self/newtable/setlist 是 (base,pc,a,b,c)
	// = i32×5→i32 = typeConcat;getglobal/setglobal 是 (base,pc,a,bx) = i32×4→i32 = typeUnm。
	p = append(p, importFuncEntry("host", "h_gettable", typeConcat)...)
	p = append(p, importFuncEntry("host", "h_settable", typeConcat)...)
	p = append(p, importFuncEntry("host", "h_getglobal", typeUnm)...)
	p = append(p, importFuncEntry("host", "h_setglobal", typeUnm)...)
	p = append(p, importFuncEntry("host", "h_self", typeConcat)...)
	p = append(p, importFuncEntry("host", "h_newtable", typeConcat)...)
	p = append(p, importFuncEntry("host", "h_setlist", typeConcat)...)
	p = append(p, importFuncEntry("host", "h_call", typeCall)...)
	p = append(p, importFuncEntry("host", "h_tailcall", typeConcat)...)   // (i32×5→i32 status)
	p = append(p, importFuncEntry("host", "h_closure", typeUnm)...)       // (i32×4→i32 status)
	p = append(p, importFuncEntry("host", "h_close", typeForPrep)...)     // (i32×3→i32 status)
	p = append(p, importFuncEntry("host", "h_tforloop", typeTForLoop)...) // (i32×4→i64)
	p = append(p, importFuncEntry("host", "h_callerr", typeCallErr)...)   // ()→()  PW10 R3

	return sectionOf(0x02, p)
}

// functionSection 声明本地函数(入口 run)的 type 索引。
// 入口是 import 之后的 function index = numHelpers(4)。
func functionSection() []byte {
	var p []byte
	p = append(p, uleb32(1)...)         // count = 1 本地函数
	p = append(p, uleb32(typeEntry)...) // 入口用 type 4
	return sectionOf(0x03, p)
}

// exportSection 导出入口函数 "run"。
func exportSection() []byte {
	var p []byte
	p = append(p, uleb32(1)...) // count
	name := "run"
	p = append(p, byte(len(name)))
	p = append(p, name...)
	p = append(p, 0x00)                  // kind=func
	p = append(p, uleb32(numHelpers)...) // 入口 function index = 4(import 后第一个本地)
	return sectionOf(0x07, p)
}

// codeSectionEntry 入口函数 code(local decl + body + end)。
func codeSectionEntry(body []byte) []byte {
	// local decl(顺序决定 local index,param $base=0 之后):
	//   组1: 2×i64 → index 1,2(localI64a/localI64b)
	//   组2: 2×i32 → index 3,5(localI32 helper status / localI32b 表地址)
	//   组3: 1×f64 → index 4(localF64,算术结果)
	//   组4: 1×i64 → index 6(localI64c,PW5 键/槽值中转)
	// 注:local index 由声明顺序决定(组内连号)。组2 声明 2×i32 占 3,4? 否——
	// 组顺序即 index 顺序:组1 i64 占 1,2;组2 i32 占 3,4;组3 f64 占 5;组4 i64 占 6。
	// 为保持 PW2-PW4 既有 index(localI32=3 / localF64=4)不变,组顺序须为:
	//   组1 2×i64(1,2) / 组2 1×i32(3) / 组3 1×f64(4) / 组4 1×i32(5) / 组5 1×i64(6)。
	var locals []byte
	locals = append(locals, uleb32(5)...) // 5 个 local 组
	locals = append(locals, uleb32(2)...) // 2 个 i64 → index 1,2
	locals = append(locals, wvtI64)
	locals = append(locals, uleb32(1)...) // 1 个 i32 → index 3(localI32)
	locals = append(locals, wvtI32)
	locals = append(locals, uleb32(1)...) // 1 个 f64 → index 4(localF64)
	locals = append(locals, wvtF64)
	locals = append(locals, uleb32(1)...) // 1 个 i32 → index 5(localI32b,PW5 表地址)
	locals = append(locals, wvtI32)
	locals = append(locals, uleb32(1)...) // 1 个 i64 → index 6(localI64c,PW5 键/槽值)
	locals = append(locals, wvtI64)

	funcBody := append([]byte{}, locals...)
	funcBody = append(funcBody, body...)
	funcBody = append(funcBody, opEnd)

	var p []byte
	p = append(p, uleb32(1)...) // count = 1 函数
	p = append(p, uleb32(uint32(len(funcBody)))...)
	p = append(p, funcBody...)
	return sectionOf(0x0a, p)
}

// --- section / import 编码 helper ---

func sectionOf(id byte, payload []byte) []byte {
	out := []byte{id}
	out = append(out, uleb32(uint32(len(payload)))...)
	return append(out, payload...)
}

// importEntry 拼一个 import 项:mod_name + field_name + kind + desc。
//   - memory: kind=0x02, desc=limits(flags+min[+max])
//   - func:   kind=0x00, desc=type index(uleb)
func importEntry(mod, name string, kind byte, desc []byte) []byte {
	var p []byte
	p = append(p, byte(len(mod)))
	p = append(p, mod...)
	p = append(p, byte(len(name)))
	p = append(p, name...)
	p = append(p, kind)
	p = append(p, desc...)
	return p
}

func importFuncEntry(mod, name string, typeIdx uint32) []byte {
	return importEntry(mod, name, 0x00, uleb32(typeIdx))
}

// uleb32 unsigned LEB128(module 组装用,与 emitter.uleb 同算法,独立函数
// 避免依赖 emitter 实例)。
func uleb32(v uint32) []byte {
	var out []byte
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			c |= 0x80
		}
		out = append(out, c)
		if v == 0 {
			return out
		}
	}
}
