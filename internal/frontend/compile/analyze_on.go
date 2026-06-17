//go:build wangshu_profile

// 当 wangshu_profile build tag 启用时,Compile 在每个 FuncExpr 编完后调
// bridge.AnalyzeProto 把可编译性分析结果写进 Proto.Compilability +
// Proto.CompReasons(`docs/design/p2-bridge/03-compilability-analysis.md`
// §6.3 接线)。
package compile

import (
	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/frontend/ast"
)

// analyzeCompilability 由 compileFunc 在产 Proto 后调用——AST 用完即弃
// (03 §2.4 决策方案 ①):本函数返回后 fn 引用即可被 GC。
//
// 不持有 Bridge 实例:用一个临时 Bridge 跑 AnalyzeProto,目的只是借用它的
// visitor + reasonsBitmap → Proto 字段写入逻辑。AnalyzeProto 内部既写
// Bridge 的 profileTable(本函数退出即被 GC)也写 Proto.Compilability /
// Proto.CompReasons(跨 State 共享只读)。
//
// 简化方案的动机:Compile 期不知道哪个 State 会 LoadProgram 用本 Proto,
// 用一个临时 Bridge 让 AnalyzeProto 复用主写入路径,代价只是丢弃一份
// 短命 profileTable 项。生产形态可能引入「全局 Compilability 写入函数」
// 直接绕开 Bridge,但当前简化够用。
//
// **F7 行为**:本临时 Bridge 没注入 P3 编译器(b.p3 == nil)→ F7 永远
// 触发 → 所有 Compile-期分析的 Proto 都被标 CompNotCompilable +
// ReasonBackendUnsupp。这反映「编译期我们还不知道运行期会注入哪个 P3」
// 的现实。**运行期 considerPromotion 看到此占位位 + b.p3 已注入时调
// bridge.recheckCompilabilityRuntime 重判**(issue #18 修复),清掉编译期
// 烧的 F7 占位,对真实后端重查 SupportsAllOpcodes,F1-F6 结构性排除原样保留。
//
// **对 PB7 验收的影响**:byte-equal 差分仍成立——P1-only build(无
// wangshu_profile)整链路 no-op;p3 build 下结构性 NotCompilable(F1-F6)
// 仍永久解释,只是「编译期 F7 占位 + 运行期 P3 可处理」的子集才走升层
// 路径,与原 byte-equal 期望一致(F1-F6 子集行为不变)。
func analyzeCompilability(fn *ast.FuncExpr, proto *bytecode.Proto) {
	tmp := bridge.NewBridge()
	tmp.AnalyzeProto(fn, proto)
}
