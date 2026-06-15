package p3indirect

// S-B 用 module 生成:验证「增量升层 = 重编整个 module + 新实例热交换」的
// 生命周期可行性。核心物理事实(承 PW1 / 03-memory-model):所有 gibbous
// module 实例 import 同一块 `env.memory`(arena 收养的 linear memory),故跨
// 实例共见同一块内存——module 实例可多份共存,内存是它们的公共底座。
//
// S-B module 形态:import env.memory(共享底座)+ 一个 driver(base) 把
// leaf(i) 累加写回 memory[base],供跨实例可见性验证。可选 import host.h_promote
// (re-entrant 升层钩:driver 执行中途回 Go 触发「编译新 module + 实例化 +
// 调用」,模拟『A 帧在 Go 栈上时升层 B』)。

// buildMemModule 生成一个 import env.memory 的 module:
//
//	driver(base) {
//	  acc = 0
//	  for i = numLeaves down to 1 { acc += leaf(i) }   // leaf(x)=x*3+1
//	  if withPromote { call h_promote }                // re-entrant 升层钩
//	  i64.store memory[base] = acc                     // 写共享内存(跨实例可见)
//	  return acc
//	}
//
// 函数索引布局(withPromote 时):func 0 = imported h_promote;leaf 1..numLeaves;
// driver = numLeaves+1。非 withPromote:leaf 0..numLeaves-1;driver = numLeaves。
func buildMemModule(numLeaves int, withPromote bool) []byte {
	if numLeaves < 1 {
		numLeaves = 1
	}
	var b []byte
	b = append(b, 0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00)

	// Type section:type0 (i32)->(i32) 给 leaf/driver;type1 ()->() 给 h_promote。
	b = append(b, sec(0x01, concat(
		uleb(2),
		[]byte{0x60, 0x01, 0x7f, 0x01, 0x7f}, // type0 (i32)->(i32)
		[]byte{0x60, 0x00, 0x00},             // type1 ()->()
	))...)

	// Import section:env.memory(必),host.h_promote(可选)。
	leafBase := uint32(0)
	imp := []byte{}
	nImp := uint32(1)
	imp = append(imp, importMemEntry("env", "memory")...)
	if withPromote {
		imp = append(imp, importFuncEntry("host", "h_promote", 1)...) // type1 ()->()
		nImp++
		leafBase = 1 // imported func 占 index 0
	}
	b = append(b, sec(0x02, concat(uleb(nImp), imp))...)

	driverIdx := leafBase + uint32(numLeaves)

	// Function section:numLeaves leaf + 1 driver,全 type0。
	fn := uleb(uint32(numLeaves + 1))
	for i := 0; i <= numLeaves; i++ {
		fn = append(fn, uleb(0)...)
	}
	b = append(b, sec(0x03, fn)...)

	// Export section:driver。
	b = append(b, sec(0x07, concat(
		uleb(1),
		[]byte{0x06}, []byte("driver"),
		[]byte{0x00}, uleb(driverIdx),
	))...)

	// Code section:leaf ×numLeaves + driver。
	var bodies [][]byte
	for i := 0; i < numLeaves; i++ {
		bodies = append(bodies, leafBody())
	}
	bodies = append(bodies, memDriverBody(leafBase, numLeaves, withPromote))
	code := uleb(uint32(len(bodies)))
	for _, body := range bodies {
		code = append(code, concat(uleb(uint32(len(body))), body)...)
	}
	b = append(b, sec(0x0a, code)...)
	return b
}

// memDriverBody — driver(base):Σ leaf(i) 累加 → i64.store memory[base];可选
// 中途 call h_promote。leafBase = 第一个 leaf 的 func index。
func memDriverBody(leafBase uint32, numLeaves int, withPromote bool) []byte {
	// locals:$acc i32(local 1)。param $base = local 0。
	locals := []byte{0x01, 0x01, 0x7f}

	body := []byte{
		0x02, 0x40, // block $exit
		0x41, 0x00, // i32.const 0
		0x21, 0x01, // local.set $acc = 0
		// 用 $base 作循环计数?不——$base 要留作 store 地址。用一个额外思路:
		// 简化:直接 call 第一个 leaf 累加 numLeaves 次(展开),避免再开 local。
	}
	// 展开 numLeaves 次 leaf 调用(numLeaves 小,S-B 用 1;manyleaves 另测编译成本)。
	for i := 0; i < numLeaves; i++ {
		body = append(body,
			0x20, 0x01, // local.get $acc
			0x41, byte(i+1), // i32.const (i+1)  —— leaf 的 arg(i<127 单字节 sleb)
			0x10, // call
		)
		body = append(body, uleb(leafBase+uint32(i))...) // leaf func index
		body = append(body, 0x6a, 0x21, 0x01)            // i32.add; local.set $acc
	}
	if withPromote {
		body = append(body, 0x10)       // call
		body = append(body, uleb(0)...) // h_promote = imported func 0
	}
	body = append(body,
		0x0b, // end block $exit
		// memory[base] = (i64)acc
		0x20, 0x00, // local.get $base (store addr)
		0x20, 0x01, // local.get $acc
		0xac,             // i64.extend_i32_s
		0x37, 0x03, 0x00, // i64.store align=3 offset=0
		0x20, 0x01, // local.get $acc (return)
	)
	return concat(locals, body, []byte{0x0b}) // + end func
}

// importMemEntry — import mod.name : memory(limits flags=0 min=1)。
func importMemEntry(mod, name string) []byte {
	return concat(
		[]byte{byte(len(mod))}, []byte(mod),
		[]byte{byte(len(name))}, []byte(name),
		[]byte{0x02},       // kind=memory
		[]byte{0x00, 0x01}, // limits flags=0, min=1
	)
}
