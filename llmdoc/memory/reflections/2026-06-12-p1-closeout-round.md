# P1 收尾轮(「已知简化」清单全量落地)

- **日期**:2026-06-12
- **任务类型**:简化项收尾(table 存储/IC/pattern/stdlib/协程/错误目录/弱表/arena ABI/difftest 生成器)

## 任务

继 M0-M14 主线交付后,把 implementation-progress.md「已知简化」清单全部不打折扣落地。
提交序列:`1ab4beb`(arena 原生表存储+generic for)→ `c04010e`(IC 命中路径)→
`c167ad6`(pattern matcher)→ `72a09d5`(table/os/io stdlib)→ `c0f2ba9`(协程)→
`473e4dd`(错误前缀+traceback)→ `7078fcf`(弱表+finalizer)→ `5122ae8`(arena ABI)→
`e1ddf2f`(difftest 随机生成器)→ `1c676b9`(测试扩充)→ `5ad59fc`(文档对账)。
落地内容与设计文档 § 对照见 implementation-progress.md「收尾轮」表,本文只记过程教训。

## 预期 vs 实际

- 预期:按设计文档各 § 逐项落地,形态与文档一致。
- 实际:全部落地且 `make all`/difftest 全绿,但三处与文档形态有意偏离(协程信号、IC 校验、
  LoadProgram 拷贝策略),其中两处是文档本身的缺口/未尽推论;一处生成器初版产非法程序返工。

## 做对了什么(可复用模式)

1. **「对称机制可复用通道」**:08 §3.3 要求 executeSignal 三态枚举,实际用 `errYieldSentinel`
   哨兵复用既有 `*LuaError` 显式返回通道实现等价语义——依据正是 08 §3.4 自己论证的
   「yield 冒泡与 error 冒泡完全对称」。**当设计文档给出「机制 A 与 B 对称」的论证时,
   实现可直接让 B 复用 A 的通道,省一类返回类型改造。** 配套纪律:doCall 捕获哨兵时必须
   一次性记全 pendingResume(ciIndex/dst/nresults/entryDepth)——恢复点信息一处不齐,
   resume 就接不回 yield 处的 CALL 结果寄存器。
2. **跨 thread upvalue 靠 uvOwner 表**:协程捕获主线程局部时,开放 upvalue 的 stackIdx
   指向**属主 thread** 的栈而非当前 running thread 的栈。01 §5.4 的 `(threadRef, stackIdx)`
   二元组设计正是为此;P1 的 Go 侧形态是 uvOwner map(uv → 属主 thread)。没有它,
   跨协程闭包读写会落到错误的栈。这是设计文档已有、但实现时容易因「单线程惯性」漏掉的点。
3. **基准印证设计预判**:IC + Program 装载缓存让 simple/arith 档从 2.3x 升到 3.1-3.2x;
   loop 档维持 2.28x——FORLOOP 回边本就不走 IC,印证 05 §3 「去装箱是主力、IC 是辅力」
   的结构:IC 收益集中在表访问密集档,数值循环档与它无关。

## 什么出了问题 / 根因

1. **IC 命中返回错值(conformance closure_per_capture 当步捕获)**:05 §6.3 的命中校验只写
   「同表 + 同代次」,但 GETTABLE 的 key 是动态 RK,同一 pc 轮换不同 key 是常态(`t[i]`
   循环)。按文档实现导致 `t[2]` 命中 `t[1]` 的缓存槽返回错值。修复:array 命中必须验
   `arrayIndex(key)==Index`,node 命中必须验 `NodeKey==key`。根因:文档以 GETGLOBAL
   (key 是常量,同 pc 必同键)为心智模型写校验条款,泛化到动态 key 指令时漏了同键维度。
2. **LoadProgram 共享导致跨 State 串台风险**:Program 跨 State 共享时,IC(运行期可写)与
   Consts(intern 进各自 arena 的 GCRef)不能共享。修复:State 私有浅拷贝——共享只读的
   Code/StringLits/LineInfo,私有化 Consts/IC/Protos。根因:11 §1.3 「惰性 intern」定稿
   只覆盖了常量归属;IC 是后来落地的运行期可写状态,设计时无此问题,推论没人补写。
   **规则:Program 上任何「运行期可写」字段都必须随 State 私有化,新增字段时过一遍此检查。**
3. **生成器初版产非法程序(oracle 报错)**:difftest 随机生成器让 if 语句对任意局部赋数值,
   生成「字符串变量 + 数字」的非法算术。修复:局部变量池按 num/str 分型,语句只在同型池
   内取变量。根因:受控文法生成器的核心纪律是**类型封闭**——每条产生式的输出类型必须落回
   池的同一型别,初版只控制了语法形状没控制类型流。
4. **覆盖率数字一度误导**:新增 ~2800 行功能代码使覆盖率 81.8% → 64.6%,测试扩充后回升
   68.6%。这不是质量退化:功能高速增长期覆盖率绝对值无意义,有效信号是「每个 feat commit
   自带对应测试」与「测试套全绿」。盯绝对值会诱发为凑数写低价值测试。

## 缺失的文档或信号

- 05 §6.3 IC 命中校验缺「同键」维度(动态 key 指令必须验 key,见上)——已在
  implementation-progress.md 对账,但**设计文档本身值得回填此条款**,否则 P2 编译层
  按 05 重新实现 IC 会再踩一次。
- 11 §1.3 未推论到「Program 运行期可写字段须 State 私有」的一般规则(当时只有常量一个案例)。
- 08 §3.3 的三态枚举与 §3.4 的对称论证之间,文档没有点破「可直接复用 error 通道」这条
  实现捷径(属低优先级:实现形态差异已在 implementation-progress.md 对账表记录)。
- 12 §3.2 生成器设计未写「类型封闭」纪律,初版踩坑后才显式化。

## Promotion 候选

- **回填设计文档(优先,recorder 执行)**:
  ① 05 §6.3 增补 IC 同键校验条款(array 验 index、node 验 NodeKey,附 `t[i]` 轮换反例);
  ② 11 §1.3 增补「Program 运行期可写字段一律 State 私有浅拷贝」一般规则(IC/Consts 实例);
  ③ 12 §3.2 增补生成器「类型封闭」纪律(分型局部池)。
- **暂留 memory**:「对称机制复用通道」模式(一次实战,P2/P3 再遇同构论证时验证);
  uvOwner 教训(设计文档 01 §5.4 已有,属实现提醒非文档缺口);覆盖率解读
  (项目级流程经验,若第二次出现可考虑进 guides 的实现冲刺工作流)。
- **不需推广**:pendingResume 四元组细节(已随代码与 implementation-progress.md 固化)。

## 后续行动

- 请 recorder 执行上面三项设计文档回填(可与上一篇 sprint 反思的三项回填合并为一轮)。
- P2 编译层实现 IC 时,以回填后的 05 §6.3 为准,并复用 closure_per_capture 等 conformance
  用例作为同键校验的回归锚点。
- 后续给 Program 增加任何字段时,先过「运行期可写 → State 私有」检查再合入。
