# 文档缺口

> recorder 维护。记录已知但当前无法稳定成文的缺口。随实现/设计推进收敛。
> 项目状态:**P1 已落地(M0-M14,总验收通过),P2+ 未开始**。早期「源文档只给概念未给 spec」类缺口已大半被 `docs/design/` 详细设计填补;「无代码可验证」类缺口已随 P1 交付收口。

## 当前缺口

- **P1 已知简化清单(指针)** — P1 范围内有一批「接口已定、实现可换」的简化(table 走 Go map 旁路、值栈用 Go slice、IC 未读、协程/pattern matcher/arena ABI 列接口/xpcall/弱表未实现等)。**唯一清单与设计文档落点在 `docs/design/p1-interpreter/implementation-progress.md`「已知简化」表**,后续替换以该表为工单来源,此处不搬运。各 llmdoc 架构文档描述的仍是目标设计,读到与简化清单冲突处以清单为实现现状。

- **三项设计文档回填候选(P2 开工前,recorder 执行)** — 源自 `reflections/2026-06-12-p1-implementation-sprint.md` 的 promotion 候选,是设计文档(非 llmdoc)回填:
  1. `04-frontend-parser-codegen.md` 补「lcode.c 同构必须到 helper 层」纪律 + 四个实例坑(goIfTrue 对 eJmp 须 invertJmp、luaK_infix 时机的左操作数提前物化、patchListAux 让无主 TESTSET 退化为 TEST、fixJump 不得硬编码 JMP opcode);
  2. `05-interpreter-loop.md` 补 ci 指针刷新不变式(所有可能重入的 opcode 之后 `ci = currentCI(th)`,根因是 Go slice append 搬迁使 C 式指针稳定性假设失效);
  3. `engineering.md` oracle 供给节补源码编译实操路径(brew 无 lua@5.1,源码编 lua-5.1.5 `make posix` 装 `~/.local/bin/lua5.1`;difftest 在 oracle 缺失时 skip 不挡 CI)。

- **差分 fuzz 随机生成器未接** — difftest 当前是 seed corpus 固定脚本(33 用例),`docs/design/p1-interpreter/12-testing-difftest.md` §3.2 规划的随机脚本生成器尚未接入;nightly-diff-fuzz 的「持续 fuzz」语义因此尚未完全兑现。

- **校准测量原始数据未入库** — roadmap §附注称原始数据与方法留存于发起方仓库工作区。`benchmarks/baseline/` 目录已随 P1 建立(三档 wangshu vs gopher-lua 基准),但**最初的跨栈校准测量原始数据(gopher-lua/LuaJ/LuaJIT Horner 对照)仍未入库**。入库后可在 [[design-premises]] / [[glossary]] 增加可追溯链接。

- **倍率两套坐标系** — 流水线图倍率(P3「4-8x」、P4「trace 收益 ~70%」)与阶段正文验收门槛(P3「相对 P1 再 ≥2x」、P4「≥ LuaJ-luajc 档」)不在同一坐标系。已在 [[evolution-roadmap]] 分列两栏并加坐标系警告;若后续源文档统一口径,应回收此提示。

- **人力估算口径不统一** — P1/P2/P3 用「人月」,P4/P5 用「人年」且 P5 标「开放式」;无团队规模或日历时间换算。如实保留。

- **P1 各文档的开放缺口分散在各篇 §缺口节** — 13 篇 P1 文档各自带「风险与缺口」节,汇总入口是 `docs/design/p1-interpreter/00-overview.md` §6;12 中标「待差分核对」的措辞已在 M14 部分兑现(5 个语义偏差被捕获修复,见 implementation-progress.md),其余随简化清单推进收敛。

- **stdlib 提供面逐函数核对待兑现** — 评审轮已定「默认面 = gopher-lua 的 OpenLibs 提供面」(见 `decisions/2026-06-11-design-review-decisions.md` 第 6 项);P1 当前只落了 base/math/string 最小集,与 gopher 提供面的逐函数核对清单(`docs/design/p1-interpreter/10-stdlib.md` §4.7)仍待落实。

- **P3 开工前置确认(待办)** — P3 开工前须向首个宿主确认「列内核是否跑在协程里」,决定协程不升层是否成立(决策第 7 项;`docs/design/p3-wasm-tier.md` §5.4)。依赖宿主,设计期无法收口。

- **engineering.md 的脚本协议待定稿** — `fuzz-triage.sh` 的 FAIL/INFRA 分类判据、非 Ubuntu runner 的 oracle 源码编译缓存、bench-gate 回退阈值、agentic workflows 接入时机,均待随 fuzz 生成器/CI 演进校准(见 `docs/design/engineering.md` §7)。

- **CI runner Node 20→24 迁移期** — GitHub 已宣布 2026-09-16 移除 runner 上的 Node 20。当前 ci.yml 用的 `actions/checkout@v4` / `actions/setup-go@v5` / `actions/upload-artifact@v4` 跑在 Node 20 上,会有弃用警告。无须现在动,2026-09 前升 action 主版本即可(目前主版本 v4/v5 已是最新,upstream 推 Node 24 时跟随升)。

## 已收口(留作审计)

- ~~无任何代码可交叉验证~~ — P1 已交付(M0-M14):`internal/` + `wangshu.go` + conformance 28 用例 + difftest 33 用例 + 三档基准全部落地,验收过线。各 llmdoc 状态措辞已同步为「P1 已落地,P2+ 未开始」;原条目预告的「构建/测试 guides」暂未立(`make all` 单入口 + engineering.md 已覆盖,第二次实现冲刺时再评估是否成文,见 2026-06-12 反思 promotion 节)(2026-06-12)。
- ~~go.sum 缺失警告(CI cache restore 失败)~~ — M14 引入 gopher-lua v1.1.2(差分基准)后 go.sum 已产出,警告自然消解(2026-06-12)。
- ~~arena ABI 字段级 spec 缺失~~ — 已由 `docs/design/p1-interpreter/11-embedding-arena-abi.md` §3-§6 定稿(列描述符/字符串区 offset 表+字节池/presence bitmap/args 与 arena 关系/句柄表/per-item API)。[[embedding-contract]] 的缺口标注已同步为指针(2026-06-11)。
- ~~P2 无独立量化验收门槛~~ — `docs/design/p2-bridge.md` §0 已正面定调:P2 是分层决策基建、不在执行热路径发力,「无量化门槛」是定位而非疏漏。[[evolution-roadmap]] 标注保持一致,不再算缺口(2026-06-11)。
