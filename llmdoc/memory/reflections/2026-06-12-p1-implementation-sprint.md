# P1 实现冲刺(M8-M14 单会话收口,总验收通过)

- **日期**:2026-06-12
- **任务类型**:大型实现冲刺(codegen → 解释器 → GC 接入 → API/stdlib/元表 → 测试验收)

## 任务

单一会话内完成 P1 余下 7 个里程碑 M8-M14(M0-M7 已在前序会话完成),实际提交顺序
M8→M9→M10→M13→M12→M11→M14(非编号序;M13 公共 API 先于 stdlib/元表完成)。
P1 总验收通过:三档基准 ≥2x over gopher-lua(simple 2.28x、arith 2.40x、loop 2.30x),
与官方 Lua 5.1.5 差分差分测试逐字节一致。进度表、验收数字与已知简化清单见
`docs/design/p1-interpreter/implementation-progress.md`,本文只记过程教训。

## 预期 vs 实际

- 预期:按设计文档逐里程碑完成,单测 + 黄金字节码测试护航即可。
- 实际:全链跑通且验收过线,但 M14 conformance/difftest 一上线即捕获 **5 个此前单测全部
  漏掉的语义 bug**,集中跨层返工(parser 补 ast.ParenExpr / codegen / 解释器)。若按
  00-overview 自家建议在 M9 后即搭 difftest,这些偏差会在引入当步被拦下。

## 做对了什么(可复用模式)

1. **「简化实现 + 接口留口」换吞吐**。设计要求 table 住 arena 哈希、值栈住 arena 视图;
   本次为在预算内跑通全链,table 用 Go map 旁路(tableSide)、值栈用 Go slice——但接口
   形状(tableGet/tableSet/enterLuaFrame)与设计文档对齐,后续替换内部实现不动调用方。
   前提:简化必须显式落盘(implementation-progress.md「已知简化」表),否则会被误当定稿。
2. **差分测试 oracle 是语义正确性的唯一可靠防线**。黄金字节码测试只防结构性偏差;5 个语义
   bug(rawEqual 的 NaN bits 比较、%.14g 的 inf/nan 措辞、and/or 对 VCALL 的单值收敛、
   VARARG 落点回填、括号强制单值)全部由差分测试捕获、当步修复,单测一个都没拦住。
3. **基准实证设计前提**。NaN-box 去装箱即使带旁路 map table 也过 2x 门槛,印证 05 §3
   「去装箱是主力、table 布局是次级优化」——后续优化排序可据此安排,不必先啃 arena 哈希。
4. **oracle 供给实操路径**:brew 无 lua@5.1(已 EOL),源码编译 lua-5.1.5(`make posix`)
   装到 `~/.local/bin/lua5.1` 即可;difftest 在 oracle 缺失时 skip 不挡 CI。

## 什么出了问题 / 根因

1. **difftest 拖到 M14 才搭,违背 00-overview「M9 后即搭」的自家建议**。根因:把 harness
   当「测试里程碑的交付物」而非「每步开发的护栏」,被「先跑通再测」惯性盖过,且无机制强制。
2. **codegen 与 lcode.c「半同构」是 bug 温床**——同构必须落到 helper 函数结构,只对齐
   opcode 输出形状不够。本次踩到 4 个:
   - goIfTrue 对 eJmp 须 invertJmp:比较指令产 A=1「真则跳」,if-then 需 A=0「假则跳」;
   - 左操作数必须在编译右子表达式**之前** exp2RK 物化(luaK_infix 时机),否则右子的
     CALL 覆盖左结果寄存器;
   - dischargeJpc/patchList 必须走 patchListAux,让无主 TESTSET(A=255 占位)退化为
     TEST,否则运行期写 R255 越界;
   - fixJump 不得硬编码 JMP opcode(会把 FORPREP 改写成 JMP),须 SetSBx 保留原 op。
   其中 luaK_posfix 同构缺口(and/or 对 VCALL 未单值收敛)直接产出 JMP sBx=-1 死循环。
3. **解释器 ci 指针失效**:th.cis 是 Go slice,host 函数(如 pcall)内部重入 execute 会
   append 触发底层数组搬迁,主循环持有的 *callInfo 旧指针失效。根因:把 C 实现的指针
   稳定性假设带进 Go slice。规则:所有「可能重入」的 opcode(CALL、带 __index handler
   的 GETTABLE、带元方法的算术)之后必须 `ci = currentCI(th)` 刷新。

## 缺失的文档或信号

- 00-overview 写了 difftest 搭建时机,但里程碑推进无 checklist 强制它,建议被惯性跳过。
- 「lcode.c 同构必须对齐到 helper 层」这一粒度要求,设计文档 04 未明示;4 个坑全属
  「输出看似对、helper 结构不对」类。
- 「C→Go 移植的指针稳定性假设失效(slice append 搬迁)」无任何提示,靠运行期崩溃发现。

## Promotion 候选

- **回填设计文档(优先,recorder 执行)**:① lcode.c 同构 4 坑回填 04(加「同构必须到
  helper 层」纪律 + 实例);② ci 刷新规则回填 05(重入 opcode 清单 + 不变式);
  ③ oracle 源码编译路径回填 engineering.md 的 oracle 供给节。
- **`guides/`(第二次实现冲刺时成文)**:「实现冲刺工作流」——difftest harness 先于功能
  推进上线(把时机建议升格为强制 checklist 项)、简化实现+接口留口+简化清单显式落盘。
  目前一次实战,暂留 memory。
- **`architecture/value-representation` 增补一行(低成本)**:2x 门槛已被去装箱单独实证
  (带 map 旁路 table),作为设计前提的实测确认。
- **暂留 memory**:5 个语义 bug 的具体清单(已录 implementation-progress.md)、本次提交
  序列细节。

## 后续行动

- P2 开工前请 recorder 执行上面三项设计文档回填。
- P2 起任何新执行层(直译编译)第一周即接入既有 difftest harness,不再后置。
- 替换 table 旁路/值栈 slice 时,以 implementation-progress.md「已知简化」表为工单来源,
  完成一项划掉一项。
