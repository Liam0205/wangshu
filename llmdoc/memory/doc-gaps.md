# 文档缺口

> recorder 维护。记录已知但当前无法稳定成文的缺口。随实现/设计推进收敛。
> 项目状态:**P1 完整交付(M0-M14 + 收尾轮 + 测试加固轮 + 完整性补全轮 + 长稳承诺轮 + 外部审查修复轮 + 官方测试套与性能轮),P2+ 未开始**。早期「源文档只给概念未给 spec」类缺口已大半被 `docs/design/` 详细设计填补;「无代码可验证」类缺口已随 P1 交付收口;原「已知简化」清单已在收尾轮全部落地。

## 当前缺口

- **设计文档回填待办(P2 开工前,recorder 执行,十项合一轮)** — 六轮反思的 promotion 候选合并清单,均为 `docs/design/` 回填(非 llmdoc):
  - 源自 `reflections/2026-06-12-p1-implementation-sprint.md`:
    1. `04-frontend-parser-codegen.md` 补「lcode.c 同构必须到 helper 层」纪律 + 五个实例坑(goIfTrue 对 eJmp 须 invertJmp、luaK_infix 时机的左操作数提前物化、patchListAux 让无主 TESTSET 退化为 TEST、fixJump 不得硬编码 JMP opcode;加固轮追加:「末位多值源 A 处理」即 luaK_setreturns 对应物在 stmtReturn/compileArgList/exprTable 三调用点同族踩坑——eCall 不动 A、eVararg 回填 A,须单点收口为 helper)。官方套轮追加第三维度「**快路径家族审计**」:同形态快路径在代码库多处分布,修一处时 grep 全家族——eNonReloc 直用快路径漏 `hasJumps()` 检查,exp2AnyReg 有正确写法而 stmtReturn 漏,悬空 JMP 非确定性挂死(`cbaae3f` 反例);与 helper 层(本项)、时序层(第 9 项)并列为同构纪律三维度;
    2. `05-interpreter-loop.md` 补 ci 指针刷新不变式(所有可能重入的 opcode 之后 `ci = currentCI(th)`,根因是 Go slice append 搬迁使 C 式指针稳定性假设失效);
    3. `engineering.md` oracle 供给节补源码编译实操路径(brew 无 lua@5.1,源码编 lua-5.1.5 `make posix` 装 `~/.local/bin/lua5.1`;difftest 在 oracle 缺失时 skip 不挡 CI)。
  - 源自 `reflections/2026-06-12-p1-closeout-round.md`:
    4. `05-interpreter-loop.md` §6.3 增补 IC 命中**同键校验**条款(动态 key 指令 array 命中验 `arrayIndex(key)==Index`、node 命中验 `NodeKey==key`,附 `t[i]` 轮换反例;P2 编译层实现 IC 以回填后版本为准);
    5. `11-embedding-arena-abi.md` §1.3 增补「Program 上运行期可写字段一律随 State 私有浅拷贝」一般规则(IC/Consts 实例;共享只读 Code/StringLits/LineInfo);
    6. `12-testing-difftest.md` §3.2 增补随机生成器「类型封闭」纪律(局部变量池按 num/str 分型,产生式输出落回同型池)。
  - 源自 `reflections/2026-06-12-test-hardening-round.md`:
    7. `05-interpreter-loop.md` §7.6 增补 callHost「top 恢复纪律」条款:定长结果路径必须恢复 top 到当前帧逻辑顶(对齐 5.1 `L->top = ci->top`),附反例——前一条多值 CALL(C=0)留下低 top 使后续 callLuaFromHost 脚手架覆写 TFORLOOP 迭代器三槽,症状(pairs 收到 number)离根因极远。落地修复见 `internal/crescent/host.go` (`callHost`)。
  - 源自 `reflections/2026-06-12-completeness-gap-round.md`:
    8. `12-testing-difftest.md` §3 增补「特性探测 corpus」机制:差分 fuzz 两轴正交模型(随机生成器=已实现行为的正确性,文法跟实现走对缺特性结构性失明;probe corpus=按**官方手册**逐节写,测特性面完整性)、probe 先过 oracle 纪律(每项须是 oracle 可执行的合法且确定性 5.1 程序)、新特性「probe 转绿 → 编入生成器文法」护栏闭环。落地见 `test/difftest/probes_test.go` (`featureProbes`);动因:probe 上线在 570+ 随机脚本全绿状态下一次扫出 25 个完整性缺口。**与第 9-10 项同源,回填时应写成完整的四轴防线模型**:① fuzz=已实现行为自洽 ② probe=特性面完整性 ③ 外部审查=规范同构+形态组合(见 `reflections/2026-06-12-longevity-review-fix-round.md`)④ **官方测试套**=作者写的语义断言(`test/luasuite/`,见 `reflections/2026-06-12-official-suite-perf-round.md`)——第四轴独有**负断言**能力:正向对拍只能验「该给的给了」,不能验「不该给的没给」(实证:describeReg 启发式误命名多轮正向对拍全绿,被 errors.lua 负断言抓出);移植即在前三轴全绿下扫出 20 项分歧。须一并注明**棘轮机制是常驻 CI 资产**:未登记 stopAt 的文件 fatal、豁免线只许前移、testdata 与官方逐字节一致,P2+ 换执行层时同批重跑且不得新增豁免。避免四轴内容在多处各写一半。
  - 源自 `reflections/2026-06-12-longevity-review-fix-round.md`:
    9. `04-frontend-parser-codegen.md` 增补「同构到时序层」条款,与第 1 项「helper 层同构」并列为两个维度:移植 C 代码时**操作顺序本身就是规范**——constFold 必须在 exp2RK(跳转链合流)之后,提前折叠制造「eKNum 带未决跳转链」非法中间态,TESTSET A=255 占位永不回填,`(true and 7 or -1) + 1` 一行脚本 Go panic(`4467881` 反例);改变语句顺序须证明交换律成立,否则按原序。
    10. `06-memory-gc.md` 增补「内存复用类变更配套清单」条款:资源复用会把潜伏的根管理 bug 从良性(死对象躺 arena)升级为致命(UAF/串台执行),复用类变更前先列「哪些 bug 此前良性、之后变致命」清单逐项加固——GC 根全量审计 / top 恢复纪律 / 对象尺寸单一事实源(`internal/object/size.go`)/ debugFreelist 类排障设施与特性同批落地。长稳轮(`62d4bb3`/`2eb44fb`)已实战走通一遍。官方套轮追加池化条目:「**资源池化的 API 契约必须出现在公共类型文档**」——callHost 实参池上线后「args 仅本次调用有效」的契约一度只在实现侧注释,第三方宿主注册 HostFn 看不到,违约症状(返回值被覆写)离根因极远;落地 `e5b72ab`(契约写入 `internal/crescent/host.go` (`HostFn`) 类型文档 + wangshu_trace 构建毒值防护)。契约位置决定使用者能否看见,与「注释承诺审计」呼应。**对账**:本条款的对偶面——「公共 first-class GCRef-bearing value 必须接 GC 根」——已在 issue234 反思轮升为 [[embedding-contract]] 契约级不变式条款(两次样本 kFunction/kTable 同款 pin 表零额外接根工作);**llmdoc 侧的 GCRef 根机制条款已落地**(覆盖公共 API 暴露面),**`docs/design/p1-interpreter/06-memory-gc.md` 侧的内部复用配套清单回填仍待办**(覆盖 VM 内部 freelist 面),两面同根但分别落 reference 与设计文档。

- **值栈/CallInfo arena 化属 P3** — 收尾轮对账确认:值栈/CallInfo 仍是 Go slice(05 §1.2 设计形态是住 arena),开放 upvalue 链/arena Thread 对象同批;**接口等价、迁移点已留**(`arena.Options.NewBacking` 注入点就位),物理搬迁是 P3 wazero memory 收养时的工作。唯一对账落点:`docs/design/p1-interpreter/implementation-progress.md`「与设计文档的对账」表。

- **校准测量原始数据未入库** — roadmap §附注称原始数据与方法留存于发起方仓库工作区。`benchmarks/baseline/` 目录已随 P1 建立(三档 wangshu vs gopher-lua 基准),但**最初的跨栈校准测量原始数据(gopher-lua/LuaJ/LuaJIT Horner 对照)仍未入库**。入库后可在 [[design-premises]] / [[glossary]] 增加可追溯链接。

- **倍率两套坐标系** — 流水线图倍率(P3「4-8x」、P4「trace 收益 ~70%」)与阶段正文验收门槛(P3「相对 P1 再 ≥2x」、P4「≥ LuaJ-luajc 档」)不在同一坐标系。已在 [[evolution-roadmap]] 分列两栏并加坐标系警告;若后续源文档统一口径,应回收此提示。

- **人力估算口径不统一** — P1/P2/P3 用「人月」,P4/P5 用「人年」且 P5 标「开放式」;无团队规模或日历时间换算。如实保留。

- **P1 各文档的开放缺口分散在各篇 §缺口节** — 13 篇 P1 文档各自带「风险与缺口」节,汇总入口是 `docs/design/p1-interpreter/00-overview.md` §6;12 中标「待差分核对」的措辞已在 M14 + 收尾轮兑现(语义偏差被 difftest 捕获修复,见 implementation-progress.md),余项随上面「设计文档回填待办」收敛。

- **stdlib 提供面逐函数核对待兑现** — 评审轮已定「默认面 = gopher-lua 的 OpenLibs 提供面」(见 `decisions/2026-06-11-design-review-decisions.md` 第 6 项);收尾轮已补 table/os/io/math 全量与 string 完整面,baseenv 补全轮(`423d690`)再补 _G/_VERSION/collectgarbage/gcinfo/loadfile/dofile/load 等;豁免注册表 15 项显式登记(`test/difftest/corners_test.go` 的 `TestExemptions_Documented`,probe/exempt/approx 三类镜像 10 §11 三列)已提供部分审计面,但与 gopher 提供面的**逐函数核对清单**(`docs/design/p1-interpreter/10-stdlib.md` §4.7)仍待落实。

- **P3 开工前置确认(待办)** — P3 开工前须向首个宿主确认「列内核是否跑在协程里」,决定协程不升层是否成立(决策第 7 项;`docs/design/p3-wasm-tier.md` §5.4)。依赖宿主,设计期无法收口。

- **engineering.md 的脚本协议待定稿** — 测试加固轮已落地其中一项:nightly 长跑 + INFRA/DIVERGENCE 分流自动开 issue 已由 `.github/workflows/nightly-diff-fuzz.yml` 实现(triage 判据内联在 workflow 而非独立 `fuzz-triage.sh`);审查修复轮区间内的 `a8bdca3` 把分流判据改为**机器可读 DIVERGENCE 标记**——测试侧输出 `seed=`/`kind=` 三类标记,workflow 只 grep 该标记,不再靠 grep "byte-diff" 文本启发式;**该新判据仍未经真实失败检验**,首次真实告警时需验证。仍待定:非 Ubuntu runner 的 oracle 源码编译缓存、bench-gate 回退阈值、agentic workflows 接入时机(见 `docs/design/engineering.md` §7);engineering.md §3.2 文本与落地形态(内联 vs 独立脚本、标记协议)的差异待回填轮顺手对账。nightly-benchmark 待办现已有现成基础:`benchmarks/realworld/` 五脚本 + `benchmarks/baseline/` 三档可直接作为夜跑基准面。

- **三层禁用机制(LibsSafe/Libs/Exclude)未完整落地** — 评审轮定稿的 stdlib 三层收紧机制(`docs/design/p1-interpreter/10-stdlib.md` §12.1:LibsSafe 预设 / Libs 位掩码 / Exclude 函数级)当前只落地了单点门控 `Options.AllowFileLoad`(loadfile/dofile 默认禁用,显式开启;豁免注册表已登记);完整位掩码机制留待首个宿主接入前落地。[[embedding-contract]] 措辞已同步为「设计承诺三层、现状单点门控」。

- **【v0.1.0 → v0.1.3 累积偏移审计】P2+ 工作时收口的四项负债** — 2026-06-12 累积偏移审计(详见 [[issue234-api-gap-round-2]] 与 issue #5/#6 round-3 反思)在三轮 pineapple 接入后扫出 4 处不算硬违设但应在 P2+ 工作触达对应代码时顺手收口的负债:

  1. **A2 drop-in 绑定漂移监控** — 三轮共 7 个公共 API 改动(`Options.HideFileLoaders` / `SetContext` / `MarkGlobalsBaseline` / `ForEach` / `Register` / `SetGlobal` / `GetGlobal`)都明确写「对位 gopher-lua」,但若再加 5 个「pineapple 要、gopher-lua 有、PUC 5.1 没有」的开关,wangshu 就会从「Lua VM with drop-in option」漂移到「gopher-lua-clone with 9x perf」。**收口动作**:在 [[embedding-contract]] §宿主绑定与 drop-in 节加一行「单一驱动源接入轮数上限」纪律——同一宿主驱动的累积 issue 超 N 轮(暂记 N=4)时应暂停接入需求,做 「reverse drop-in」审计:把已加的对位开关回写进 [[evolution-roadmap]] 的 drop-in 定位段验证。P2+ 接入第二个宿主时是检验本纪律的时机。

  2. **B3 host closure 公共面对称缺口** — `Value.IsFunction() == true` 当前**不**蕴含「`state.Call(v)` 可调」:Lua closure 可,host closure(`Register` 注册的 Go fn)报错 "host closure cannot be called from Go end"(`031ec06`)。godoc 已写,但公共 API 表面破对称。**收口选项**(P2+ 二选一):① 实现 host closure 从 Go 端 Call,需在 `internal/crescent.State.Call` 入口对 host closure 加临时栈帧脚手架(callHost 已有,补 Go 端入口即可);② 公共面分裂 `IsLuaFunction()` / `IsHostFunction()`,让用户能预判。落点 `docs/design/p1-interpreter/11-embedding-arena-abi.md` §1.5 `Program.Call` 与 §10 host function 注册节回填一段「Go 端 Call host closure 的支持现状」决策。

  3. **B4 baseline 非字符串 key 静默跳过** — `MarkGlobalsBaseline` 仅快照字符串 key 全局,这是基于「stdlib 与宿主自己的全局都是字符串 key」的事实但**没有强制**——若未来 stdlib 或宿主 `Register` 加 number/table key 全局(Lua 合法但罕见),baseline 静默漏拍,Reset 后泄漏。**收口动作**(P2+):① conformance 测试集加一条「stdlib `OpenAll` 后 _G 中不含非字符串 key」固化裁口;② godoc 补 panic 路径——若 Mark 时遇到非字符串 key,fail-fast(让漏洞显形而非静默)。落点 `internal/crescent/host.go` `MarkGlobalsBaseline` + `internal/stdlib/` 任何后续新增。

  4. **B7 State 字段并发模型分级** — `ctx atomic.Pointer[ctxHolder]` 是 `*State` 上第一个跨 goroutine 字段([[embedding-contract]] §8.1 并发约定整体仍是「单 goroutine」),其余字段(arena/globals/cis/runningThread)都假设单生产单消费。实际无 bug 但语义不一致——以后再加跨 goroutine 字段(e.g., 跨 goroutine 状态查询、外部 trace hook)若不分级会让 State 字段并发模型混乱。**收口动作**:在 `internal/crescent/state.go State struct` 顶部把字段按并发等级分组(`// goroutine-local:`/`// cross-goroutine (atomic):`)注释;[[embedding-contract]] §8.1 表里加一行注明 `ctx` 字段是显式例外。落点纯文档+注释,P2+ 加新跨 goroutine 字段时强制对账。

  以上四项**不阻塞 v0.1.3 发布**,审计判定为「中-轻偏差」;P2 接 wazero 编译层时大概率触达 §1/§10/§8 三段设计文档,触达即顺手回填。

## 已收口(留作审计)

- ~~CI runner Node 20→24 迁移期~~ — 原计划 2026-09 前升 action 主版本,完整性补全轮顺手提前完成(`1379319`):ci.yml 与 nightly-diff-fuzz.yml 全部升至 Node 24 线(`actions/checkout@v6` / `actions/setup-go@v6` / `actions/upload-artifact@v7`),弃用警告消除(2026-06-12)。
- ~~差分 fuzz 随机生成器跟实现走的结构性盲区~~ — 用户指出「官方有而我们没有的功能,diff-fuzz 测不出来;若不修,diff-fuzz 是假的」。完整性补全轮落地特性探测 corpus(`test/difftest/probes_test.go`,按官方 5.1 手册逐节,100 项全绿常驻对拍),上线即在 570+ 随机脚本全绿状态下扫出 25 个完整性缺口(元方法面/loadstring/select 负索引等),全部修复;新特性同步编入生成器文法(三期 15→19 类)形成「probe 转绿 → 进文法」护栏闭环。两轴正交模型的设计文档回填见当前缺口第 8 项(2026-06-12)。

- ~~P1 已知简化清单~~ — 收尾轮全部落地(提交区间 `1ab4beb..5ad59fc`):arena 原生表存储、IC 命中路径、协程、pattern matcher、stdlib 补全、错误前缀+traceback、弱表/finalizer、arena ABI 列接口、difftest 随机生成器。实现形态与设计文档的差异(均接口等价)见 `implementation-progress.md`「与设计文档的对账」表;P3 迁移留口另立当前缺口条目(2026-06-12)。
- ~~差分 fuzz 随机生成器未接~~ — 收尾轮落地(`e1ddf2f`):受控文法随机脚本生成器 + 200 确定性种子,对拍官方 5.1.5 全部逐字节一致;12 §3.2 的「持续 fuzz」语义已兑现。生成器「类型封闭」纪律的设计文档回填见当前缺口第 6 项(2026-06-12)。

- ~~无任何代码可交叉验证~~ — P1 已交付(M0-M14):`internal/` + `wangshu.go` + conformance 28 用例 + difftest 33 用例 + 三档基准全部落地,验收过线。各 llmdoc 状态措辞已同步为「P1 已落地,P2+ 未开始」;原条目预告的「构建/测试 guides」暂未立(`make all` 单入口 + engineering.md 已覆盖,第二次实现冲刺时再评估是否成文,见 2026-06-12 反思 promotion 节)(2026-06-12)。
- ~~go.sum 缺失警告(CI cache restore 失败)~~ — M14 引入 gopher-lua v1.1.2(差分基准)后 go.sum 已产出,警告自然消解(2026-06-12)。
- ~~arena ABI 字段级 spec 缺失~~ — 已由 `docs/design/p1-interpreter/11-embedding-arena-abi.md` §3-§6 定稿(列描述符/字符串区 offset 表+字节池/presence bitmap/args 与 arena 关系/句柄表/per-item API)。[[embedding-contract]] 的缺口标注已同步为指针(2026-06-11)。
- ~~P2 无独立量化验收门槛~~ — `docs/design/p2-bridge.md` §0 已正面定调:P2 是分层决策基建、不在执行热路径发力,「无量化门槛」是定位而非疏漏。[[evolution-roadmap]] 标注保持一致,不再算缺口(2026-06-11)。
