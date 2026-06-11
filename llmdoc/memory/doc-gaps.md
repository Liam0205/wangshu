# 文档缺口

> recorder 维护。记录已知但当前无法稳定成文的缺口。随实现/设计推进收敛。
> 项目状态:**P1 完整交付(M0-M14 + 收尾轮),P2+ 未开始**。早期「源文档只给概念未给 spec」类缺口已大半被 `docs/design/` 详细设计填补;「无代码可验证」类缺口已随 P1 交付收口;原「已知简化」清单已在收尾轮全部落地。

## 当前缺口

- **设计文档回填待办(P2 开工前,recorder 执行,六项合一轮)** — 两轮反思的 promotion 候选合并清单,均为 `docs/design/` 回填(非 llmdoc):
  - 源自 `reflections/2026-06-12-p1-implementation-sprint.md`:
    1. `04-frontend-parser-codegen.md` 补「lcode.c 同构必须到 helper 层」纪律 + 四个实例坑(goIfTrue 对 eJmp 须 invertJmp、luaK_infix 时机的左操作数提前物化、patchListAux 让无主 TESTSET 退化为 TEST、fixJump 不得硬编码 JMP opcode);
    2. `05-interpreter-loop.md` 补 ci 指针刷新不变式(所有可能重入的 opcode 之后 `ci = currentCI(th)`,根因是 Go slice append 搬迁使 C 式指针稳定性假设失效);
    3. `engineering.md` oracle 供给节补源码编译实操路径(brew 无 lua@5.1,源码编 lua-5.1.5 `make posix` 装 `~/.local/bin/lua5.1`;difftest 在 oracle 缺失时 skip 不挡 CI)。
  - 源自 `reflections/2026-06-12-p1-closeout-round.md`:
    4. `05-interpreter-loop.md` §6.3 增补 IC 命中**同键校验**条款(动态 key 指令 array 命中验 `arrayIndex(key)==Index`、node 命中验 `NodeKey==key`,附 `t[i]` 轮换反例;P2 编译层实现 IC 以回填后版本为准);
    5. `11-embedding-arena-abi.md` §1.3 增补「Program 上运行期可写字段一律随 State 私有浅拷贝」一般规则(IC/Consts 实例;共享只读 Code/StringLits/LineInfo);
    6. `12-testing-difftest.md` §3.2 增补随机生成器「类型封闭」纪律(局部变量池按 num/str 分型,产生式输出落回同型池)。

- **值栈/CallInfo arena 化属 P3** — 收尾轮对账确认:值栈/CallInfo 仍是 Go slice(05 §1.2 设计形态是住 arena),开放 upvalue 链/arena Thread 对象同批;**接口等价、迁移点已留**(`arena.Options.NewBacking` 注入点就位),物理搬迁是 P3 wazero memory 收养时的工作。唯一对账落点:`docs/design/p1-interpreter/implementation-progress.md`「与设计文档的对账」表。

- **校准测量原始数据未入库** — roadmap §附注称原始数据与方法留存于发起方仓库工作区。`benchmarks/baseline/` 目录已随 P1 建立(三档 wangshu vs gopher-lua 基准),但**最初的跨栈校准测量原始数据(gopher-lua/LuaJ/LuaJIT Horner 对照)仍未入库**。入库后可在 [[design-premises]] / [[glossary]] 增加可追溯链接。

- **倍率两套坐标系** — 流水线图倍率(P3「4-8x」、P4「trace 收益 ~70%」)与阶段正文验收门槛(P3「相对 P1 再 ≥2x」、P4「≥ LuaJ-luajc 档」)不在同一坐标系。已在 [[evolution-roadmap]] 分列两栏并加坐标系警告;若后续源文档统一口径,应回收此提示。

- **人力估算口径不统一** — P1/P2/P3 用「人月」,P4/P5 用「人年」且 P5 标「开放式」;无团队规模或日历时间换算。如实保留。

- **P1 各文档的开放缺口分散在各篇 §缺口节** — 13 篇 P1 文档各自带「风险与缺口」节,汇总入口是 `docs/design/p1-interpreter/00-overview.md` §6;12 中标「待差分核对」的措辞已在 M14 + 收尾轮兑现(语义偏差被 difftest 捕获修复,见 implementation-progress.md),余项随上面「设计文档回填待办」收敛。

- **stdlib 提供面逐函数核对待兑现** — 评审轮已定「默认面 = gopher-lua 的 OpenLibs 提供面」(见 `decisions/2026-06-11-design-review-decisions.md` 第 6 项);收尾轮已补 table/os/io/math 全量与 string 完整面,但与 gopher 提供面的**逐函数核对清单**(`docs/design/p1-interpreter/10-stdlib.md` §4.7)仍待落实。

- **P3 开工前置确认(待办)** — P3 开工前须向首个宿主确认「列内核是否跑在协程里」,决定协程不升层是否成立(决策第 7 项;`docs/design/p3-wasm-tier.md` §5.4)。依赖宿主,设计期无法收口。

- **engineering.md 的脚本协议待定稿** — `fuzz-triage.sh` 的 FAIL/INFRA 分类判据、非 Ubuntu runner 的 oracle 源码编译缓存、bench-gate 回退阈值、agentic workflows 接入时机,均待随 fuzz 生成器/CI 演进校准(见 `docs/design/engineering.md` §7)。

- **CI runner Node 20→24 迁移期** — GitHub 已宣布 2026-09-16 移除 runner 上的 Node 20。当前 ci.yml 用的 `actions/checkout@v4` / `actions/setup-go@v5` / `actions/upload-artifact@v4` 跑在 Node 20 上,会有弃用警告。无须现在动,2026-09 前升 action 主版本即可(目前主版本 v4/v5 已是最新,upstream 推 Node 24 时跟随升)。

## 已收口(留作审计)

- ~~P1 已知简化清单~~ — 收尾轮全部落地(提交区间 `1ab4beb..5ad59fc`):arena 原生表存储、IC 命中路径、协程、pattern matcher、stdlib 补全、错误前缀+traceback、弱表/finalizer、arena ABI 列接口、difftest 随机生成器。实现形态与设计文档的差异(均接口等价)见 `implementation-progress.md`「与设计文档的对账」表;P3 迁移留口另立当前缺口条目(2026-06-12)。
- ~~差分 fuzz 随机生成器未接~~ — 收尾轮落地(`e1ddf2f`):受控文法随机脚本生成器 + 200 确定性种子,对拍官方 5.1.5 全部逐字节一致;12 §3.2 的「持续 fuzz」语义已兑现。生成器「类型封闭」纪律的设计文档回填见当前缺口第 6 项(2026-06-12)。

- ~~无任何代码可交叉验证~~ — P1 已交付(M0-M14):`internal/` + `wangshu.go` + conformance 28 用例 + difftest 33 用例 + 三档基准全部落地,验收过线。各 llmdoc 状态措辞已同步为「P1 已落地,P2+ 未开始」;原条目预告的「构建/测试 guides」暂未立(`make all` 单入口 + engineering.md 已覆盖,第二次实现冲刺时再评估是否成文,见 2026-06-12 反思 promotion 节)(2026-06-12)。
- ~~go.sum 缺失警告(CI cache restore 失败)~~ — M14 引入 gopher-lua v1.1.2(差分基准)后 go.sum 已产出,警告自然消解(2026-06-12)。
- ~~arena ABI 字段级 spec 缺失~~ — 已由 `docs/design/p1-interpreter/11-embedding-arena-abi.md` §3-§6 定稿(列描述符/字符串区 offset 表+字节池/presence bitmap/args 与 arena 关系/句柄表/per-item API)。[[embedding-contract]] 的缺口标注已同步为指针(2026-06-11)。
- ~~P2 无独立量化验收门槛~~ — `docs/design/p2-bridge.md` §0 已正面定调:P2 是分层决策基建、不在执行热路径发力,「无量化门槛」是定位而非疏漏。[[evolution-roadmap]] 标注保持一致,不再算缺口(2026-06-11)。
