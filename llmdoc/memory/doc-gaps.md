# 文档缺口

> recorder 维护。记录已知但当前无法稳定成文的缺口。随实现/设计推进收敛。
> 项目状态:**设计文档集全卷齐备,无代码实现**。早期「源文档只给概念未给 spec」类缺口已大半被 `docs/design/` 详细设计填补。

## 当前缺口

- **校准测量原始数据未入库** — roadmap §附注称原始数据与方法留存于发起方仓库工作区;`docs/design/p1-interpreter/12-testing-difftest.md` §6 已规划 `benchmarks/baseline/`(三档脚本 + 复现方法)并定稿入库口径,但**数据本身仍未入库**,本仓库无此目录。入库后可在 [[design-premises]] / [[glossary]] 增加可追溯链接。

- **倍率两套坐标系** — 流水线图倍率(P3「4-8x」、P4「trace 收益 ~70%」)与阶段正文验收门槛(P3「相对 P1 再 ≥2x」、P4「≥ LuaJ-luajc 档」)不在同一坐标系。已在 [[evolution-roadmap]] 分列两栏并加坐标系警告;若后续源文档统一口径,应回收此提示。

- **人力估算口径不统一** — P1/P2/P3 用「人月」,P4/P5 用「人年」且 P5 标「开放式」;无团队规模或日历时间换算。如实保留。

- **P1 各文档的开放缺口分散在各篇 §缺口节** — 13 篇 P1 文档各自带「风险与缺口」节,汇总入口是 `docs/design/p1-interpreter/00-overview.md` §6;12 中标「待差分核对」的错误措辞等需实现期逐条兑现。不在此逐条搬运,实现期收敛。

- **无任何代码可交叉验证** — 全部内容仍为设计意图。一旦开始实现,需新增构建/测试/差分 fuzz 的 guides,并把架构文档从「规划」措辞更新为「实现现状」。

- **stdlib 提供面逐函数核对待实现期兑现** — 评审轮已定「默认面 = gopher-lua 的 OpenLibs 提供面」(见 `decisions/2026-06-11-design-review-decisions.md` 第 6 项),不再是「P1 范围待宿主确认」;但与 gopher 提供面的逐函数核对清单需实现期落实(`docs/design/p1-interpreter/10-stdlib.md` §4.7)。

- **P3 开工前置确认(待办)** — P3 开工前须向首个宿主确认「列内核是否跑在协程里」,决定协程不升层是否成立(决策第 7 项;`docs/design/p3-wasm-tier.md` §5.4)。依赖宿主,设计期无法收口。

## 已收口(留作审计)

- ~~arena ABI 字段级 spec 缺失~~ — 已由 `docs/design/p1-interpreter/11-embedding-arena-abi.md` §3-§6 定稿(列描述符/字符串区 offset 表+字节池/presence bitmap/args 与 arena 关系/句柄表/per-item API)。[[embedding-contract]] 的缺口标注已同步为指针(2026-06-11)。
- ~~P2 无独立量化验收门槛~~ — `docs/design/p2-bridge.md` §0 已正面定调:P2 是分层决策基建、不在执行热路径发力,「无量化门槛」是定位而非疏漏。[[evolution-roadmap]] 标注保持一致,不再算缺口(2026-06-11)。

- **engineering.md 的脚本协议待实现期定稿** — `fuzz-triage.sh` 的 FAIL/INFRA 分类判据、非 Ubuntu runner 的 oracle 源码编译缓存、bench-gate 回退阈值、agentic workflows 接入时机,均待 M0/M14 落地时校准(见 `docs/design/engineering.md` §7)。

- **CI runner Node 20→24 迁移期** — GitHub 已宣布 2026-09-16 移除 runner 上的 Node 20。当前 ci.yml 用的 `actions/checkout@v4` / `actions/setup-go@v5` / `actions/upload-artifact@v4` 跑在 Node 20 上,会有弃用警告。无须现在动,2026-09 前升 action 主版本即可(目前主版本 v4/v5 已是最新,upstream 推 Node 24 时跟随升)。
- **go.sum 缺失警告**(CI cache restore 失败,non-blocking)— 目前无外部依赖。M14 引入 gopher-lua(差分基准)或更早某轮引入第三方 lib 时自然产出。
