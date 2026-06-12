# 测试加固轮(fuzz/差分体系四向扩展)

- **日期**:2026-06-12
- **任务类型**:测试基建加固(go-fuzz 目标/生成器文法两期/GC 压力 fuzz/错误消息对拍/nightly workflow)

## 任务

继 P1 收尾轮后,把测试体系按四个扩展范围全部落地。提交序列:
`bf6164e`(4 个 Go fuzz 目标 + parser 深度护栏)→ `2d3940f`(生成器 10 类语句、500 种子)→
`ae448c1`(生成器二期 15 类:泛型 for/元表/协程/pattern)→ `5915f39`(GC 压力 fuzz 双模式)→
`282edb0`(错误消息 byte-equal 26 用例 + getobjname 简化版 + ParseLuaNumber 唯一入口
+ 算术 string coercion + UNM/__concat 元方法)→ `3f605f9`(nightly-diff-fuzz workflow)→
`1792af9`(残余覆盖补测;间杂 `0cece32`/`80d9c95` 覆盖与清理)。
`TestDiff_RandomScripts` 同步支持 `WANGSHU_FUZZ_SEED_BASE`/`WANGSHU_FUZZ_N` 环境变量
(固定 500 做 PR 门禁,nightly 日期滚动 2 万/晚)。

## 预期 vs 实际

- 预期:测试加固是「防御性基建」,主要产出测试代码与 workflow,不预期改实现。
- 实际:**每类 fuzz 上线当天就抓到自己负责领域的真实 bug**,共 5 个、全部当步修复——
  加固轮同时成了一轮隐藏 bug 清剿。此前的全绿有相当部分是覆盖空洞。

## 做对了什么(可复用模式)

1. **「每类 fuzz 上线当天就抓到 bug」的逆命题是最重要教训:fuzz 目标空转是最危险的
   虚假安全感**。本轮之前 `scripts/go-fuzz.sh`(自动发现 `^func Fuzz`)在仓库里
   **一个目标都找不到**,CI fuzz-smoke 打印 "no fuzz targets found, skip" 后绿色通过——
   基础设施「在跑」与防线「在防」是两回事。上线一条防线的验收标准应是「它抓到过
   注入的假 bug 或真 bug」,而非「workflow 全绿」。
2. **GC 压力模式按设计意图工作(12 §5 实证)**:弱表冲突链截断 bug 需要「多键散列冲突
   + 链中段条目死亡 + 恰好发生 GC」三条件,正常模式 GC 次数少几乎撞不中;压力模式
   (每个 safepoint 强制 full Collect)必现。同一脚本「正常 vs 压力」双模式 byte-equal
   对照是廉价而强力的 GC 透明性 oracle——P2+ 每个新执行层接入时直接复用
   (`wangshu.go` `SetGCStressMode`)。
3. **nightly 信号分流设计**:真分歧(grep byte-diff → label bug)与环境失败(oracle
   不可用 → label ci)分流开 issue 不混信号;同标题去重(已有 open issue 则评论追加);
   固定种子防回归、日期滚动种子拓新,失败可单种子精确重放。撞出的分歧按 12 §3.6
   最小化回流 conformance,回归不靠再次随机撞中。
4. **错误消息以官方为字节级 oracle 的成本比预想低**:26 用例归一化位置前缀后逐字节;
   getobjname 只需简化版(local/global/field/method 四类 + MOVE 追源寄存器 + LocVars
   回填)即可与 5.1.5 措辞一致,不需要完整的 symbolic 重放。

## 什么出了问题 / 根因

1. **「末位多值源 A 处理」同族 bug 第三处(生成器一期捕获)**:stmtReturn /
   compileArgList / exprTable 三处都要处理「末位是 eCall/eVararg」——eCall 的 A 已是
   fnReg 不可 SetA 覆盖、eVararg 须回填 A。前两处在实现冲刺已修,本轮 exprTable 撞出
   第三处(`{ 729, math.max(1, 2) }` 把 LOADK 的数字当 callee)。根因:官方在
   luaK_setreturns/luaK_setmultret 单点收口的逻辑,我们三处手写、每处独立踩坑。
   **同族修复出现第二处时就该抽 helper,不该等第三处**——这是 sprint 反思
   「lcode.c 同构必须到 helper 层」的又一实例。
2. **callHost 定长结果不恢复 top(生成器二期捕获)**:5.1 调用约定里
   `L->top = ci->top`(返回时恢复当前帧逻辑顶)是 callHost 返回路径的一部分,移植时被
   「保守做法是 top 不动」的注释绕过。症状离根因极远:前一条多值 CALL(C=0)留下低 top
   → 后续 callLuaFromHost 脚手架覆写活跃寄存器 → TFORLOOP 迭代器三槽被毁 →
   "bad argument #1 to 'next'"(pairs 收到 number)。触发形状(嵌套 math.max + 表 +
   pairs)人写不出用例,生成器二期文法上线当步撞中。教训:**top 恢复纪律是调用约定的
   组成部分,官方源码里每一行「看似清理性」的语句都是不变式,不可省略**。
3. **pattern matchCapture 位置捕获反向引用 panic(go-fuzz 捕获)**:`%1` 反向引用
   位置捕获时 capture len 是哨兵值 -2,`slice[:-2]` 越界 panic。修复:len<0 一律报
   invalid capture(对齐 5.1);crash 输入 `()%1` 入库 testdata 防回归。
4. **parser 深嵌套打爆 Go 栈(go-fuzz 捕获)**:2M 层括号直接 fatal——Go 栈溢出
   不是 panic、recover 接不住,fuzz 一撞整进程死。修复 `maxParseDepth=200`
   (parseExpr/parseBlock 进出计数,超限报 5.1 同款 "chunk has too many syntax levels")。
   教训:**递归下降 parser 在 Go 里必须有显式深度护栏**,这不是优化是生存条件。
5. **clearWeakTables 清死条目截断冲突链(GC 压力捕获)**:清 key/val 时顺手把
   next 重置 -1,链上后续**存活**条目从此查不到(物理还在、逻辑丢失,「强引用的值
   从弱表消失」)。修复:死条目保链到 rehash(与 rawSet 删除路径、5.1 一致)。
   根因:把「清条目」理解成「删条目」——开链哈希里 next 是结构不是内容。

## 缺失的文档或信号

- 05 §7.6(host function 调用约定)未写「定长结果路径必须恢复 top 到 ci->top」——
  官方源码有这行,设计文档转述时丢失,实现照文档便漏掉。**值得回填**(P2 写编译层
  调用桥会再走一遍此约定)。已登记 doc-gaps 回填待办第 7 项。
- 「末位多值源 A 处理」三处手写无单点收口,是 04 回填项「同构到 helper 层」缺位的
  直接后果——已在 doc-gaps 第 1 项校准(并入实例清单)。
- go-fuzz.sh 零目标时仅打印 skip 且 exit 0,无任何「防线为空」的红色信号。

## Promotion 候选

- **回填设计文档(并入 doc-gaps 合并轮,recorder 执行)**:05 §7.6 增补 callHost
  「top 恢复纪律」条款(定长结果恢复帧逻辑顶,附 TFORLOOP 三槽被覆写反例)——
  已登记为回填待办第 7 项。
- **既有回填项增强**:04「同构到 helper 层」回填时把「末位多值源 A 处理」
  (luaK_setreturns 对应物,三调用点同族 bug)并入实例——已在 doc-gaps 第 1 项校准。
- **暂留 memory**:「防线上线验收 = 抓到过 bug」纪律(一轮实战,P2 接新执行层防线时
  再验证是否升 guides);GC 双模式对照模式(12 §5 设计已有,本轮只是实证);
  go-fuzz.sh 零目标硬失败护栏(低优先级,当前已有 4 目标,下次动 engineering.md 顺手)。

## 后续行动

- P2 开工前 recorder 执行 doc-gaps「设计文档回填待办」七项合一轮(本轮新增第 7 项)。
- nightly 首次真实告警时验证分流 grep 判据(INFRA vs DIVERGENCE 目前是纸面设计,
  未经真实失败检验);分歧按 12 §3.6 最小化回流 conformance。
- P2 新执行层上线第一周即接全套防线:difftest harness + GC 双模式对照 + go-fuzz
  端到端目标(`fuzz_test.go` `FuzzCompileRun` 换内核即用),不再后置。
