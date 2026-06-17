# 架构:分层 VM 演进路线(P1→P5)

> 状态:**P1 已交付(M0-M14,验收过线),P2-P5 仍为规划**。源:`docs/design/roadmap.md` (§4)。**全阶段详细设计文档已就位**:P1 见 `docs/design/p1-interpreter/`(全卷 00-12,实现现状见同目录 implementation-progress.md),P2-P5 见 `docs/design/p2-bridge/`(子目录) / `p3-wasm-tier/`(子目录 9 文件)/ `p4-method-jit.md` / `p5-trace-jit.md`。
> 前置约束(为什么是分层、为什么倍率以列内核为口径):见 [[design-premises]]。值表示如何在各层共见同一块内存:见 [[value-representation]]。

## 流水线全景

```
P1 解释器 ──► P2 分层桥 ──► P3 Wasm 编译层 ──► P4 method JIT ──► P5 trace JIT
(2-4x)        (基建)        (4-8x)             (trace 收益~70%)   (10-30x,开放式)
```

- 括号内是**流水线图倍率**,口径为 **over gopher-lua,列内核负载形状下**。
- 人力从人月级(P1/P2/P3)升到人年级(P4/P5)。
- 每阶段**独立交付价值,任何闸门处停下都不亏**(贯穿原则 3,见 [[design-premises]])。

> **坐标系警告**:流水线图的倍率(如 P3「4-8x」、P4「trace 收益 ~70%」)与各阶段正文的**验收门槛**(如 P3「相对 P1 再 ≥2x」、P4「≥ LuaJ-luajc 档」)**不在同一坐标系**,不要混为一谈。下文速查表已分列两栏。

## 月相 tier 命名映射

执行层沿用月相命名(与项目名同一意象系,**代码与文档统一使用**);日志/诊断输出形如 `function promoted to gibbous`,文档称其「比裸 tier 编号自释」。

| tier | 月相名 | 对应阶段 |
|---|---|---|
| tier-0 | **crescent**(新月) | P1 解释器 |
| tier-1 | **gibbous**(凸月) | P3 Wasm 编译层 / P4 method JIT |
| tier-2 | **fullmoon**(满月) | P5 trace JIT |

**注意 tier 比阶段粗一层**:tier-1(gibbous)**同时覆盖 P3 与 P4**;**P2 是分层桥基建,不是执行层,未分配月相**。映射关系:P1→tier-0、P3+P4→tier-1、P5→tier-2。

## 速查表(人力 / 流水线图倍率 / 验收门槛 / 前置 spike)

| 阶段 | 名称 | 人力估算 | 流水线图倍率 | 关键验收门槛(正文) | 前置 spike |
|---|---|---|---|---|---|
| P1 | 现代解释器 | 6-9 人月 | 2-4x | 简单/算术/循环三档脚本全 ≥2x over gopher-lua;与官方 5.1.5 差分 fuzz 逐字节一致(gopher 偏差豁免) | 无(起点) |
| P2 | 分层桥 | 1-2 人月 | 基建(无量化) | 文档未给独立量化门槛 | 无 |
| P3 | Wasm 编译层 | 6-12 人月 | 4-8x | 循环密集脚本相对 P1 再 ≥2x;两层差分 fuzz 逐字节一致 | **wazero call boundary 实测 `<150ns`;不达标则跳过本阶段直接做 P4** |
| P4 | method JIT(JSC Baseline 风格) | +1-2 人年 | trace 收益 ~70% | 列内核负载 ≥ LuaJ-luajc 档;Wasm 层退役或留作可移植中层 | 无(继承 P3 管线) |
| P5 | trace JIT | +2-4 人年到可信 v1(开放式) | 10-30x | 列内核负载 10-30x over gopher-lua | 仅在 P4 收益不够时启动 |

## 各阶段正文

### P1:现代解释器(6-9 人月,目标 2-4x)

- lexer / parser / **寄存器式字节码**(对解释器友好,日后翻译为 Wasm locals 也直白);
- NaN-boxed 值世界 + arena + 自管 mark-sweep GC(见 [[value-representation]]);
- **closure-compilation 或 computed-goto 风格 dispatch**(替代大 switch);
- 全局/表访问 **inline cache**;stdlib 以 **host function** 形式提供;
- Lua 5.1 conformance 测试套。
- **验收**:简单/算术/循环三档脚本全部 ≥2x over gopher-lua;与官方 Lua 5.1.5 差分 fuzz 输出逐字节一致(官方为最终 oracle,gopher-lua 偏差登记豁免,见 12 号文档)。**已通过,P1 性能轮后数字(2026-06-12)**:simple 9.0x / arith 7.0x / loop 2.45x(simple 档大幅拉升主因是 State.Call 复用主 thread 消去短脚本固定开销,非解释器本身倍率);realworld 五项四项反超;70 种子 + 200 随机脚本对拍逐字节一致。实现对账与 P3 迁移留口见 implementation-progress.md。
- **独立价值**:止步于此也成立——一个「更好的 gopher-lua」,可作 drop-in 候选(见 [[embedding-contract]])。

### P2:分层桥(1-2 人月,定位为基建)

- 函数级**热度计数**(loop back-edge 计数);
- **inline cache 反馈记录**(类型 feedback,为编译层供料);
- **静态可编译性分析器**:把 varargs / coroutine / debug 等形状标记「不升层」,永远走解释。另有 **MinPromotableCodeLen 启发式守卫**(issue #21,`MinPromotableCodeLen=10`):short proto(Code 长度 <10 opcode)即便过热度阈值也不升层(profile counter 仍累积保诊断完整,只在 considerPromotion 前 return)——wasm 跨层边界成本 > 解释器收益,force-all 测试入口可绕过。见 [[2026-06-17-issue21-short-proto-guard-round]]。
- 策略:**try-compile-fallback-interpret**(LuaJ luajc 同款),换来**零 deopt 机器**。
- **验收**:文档未对 P2 给出独立量化验收(无倍率门槛,定位为基建)。

### P3:Wasm 编译层(6-12 人月,流水线图 4-8x)

- **字节码→Wasm 编译器,wazero 执行**;值世界 = linear memory(**P1 的 arena 直接映射,两层共见**);
- 跨层 **trampoline**;解释器↔编译层互调协议。
- **开工前置 spike**:**wazero call boundary 实测,目标 `<150ns`;不达标则跳过本阶段直接做 P4**。
- **验收**:循环密集脚本相对 P1 再 ≥2x;两层差分 fuzz 逐字节一致。
- **战略价值**:在不用调试机器码的后端上,先把分层机器(升层/降层/fallback)整体跑通。
- **现状(2026-06-16)**:**PW0-PW10 全卷已收口**(PW0-PW9 + PW4b spike 闸门 → 翻译器全 38 opcode 除 VARARG → 跨层互调 → 升层门禁 → V1-V18 验收;loop 核 2.58x V14 达标)。**PW10 收口**:R1-R3.5(共享 funcref 表 + CallInfo→linear-memory + `call_indirect` 直调 + host helper 零分配)+ 零跨界 ①(top mirror 字)/ 基建-a(closure slot 缓存)/ 基建-b(proto cache 段)/ ③a(savedTop)/ ③b(emitReturn 守卫快路径,Wasm 内拆帧)/ ④-i(emitCall 守卫骨架 + fastCallHits mirror 字)/ callOnStack 顶层升层(cl 直接走 enterGibbous + TopLevelUplift 探针)全付;**本机 Xeon 6982P 2s×3 count 实测基线(2026-06-16)**:loop 2.95x(+10% over R3.5 2.67x,③ RETURN 拆帧真实收益)/ table 0.88x / call 0.52x / mixed 0.99x;**call 0.52x 是 bench kernel 结构性架构边界**——profile `/tmp/call.prof` 证四 kernel body 含 ReasonUnknownCall(F2-b 静态分析不能确定被调函数不 yield)→ body 不可升 → 顶层升层 + ④ emitCall fast body 均对 bench kernel 无显著效果;**④-ii fast body 留 followup**(预估上限 0.57x 仍 <1x,实现复杂度 ~200 行 wasm 字节级 codegen,ROI/UAF 不利,emit 原语 i64.add/i64.or 已保留)。**PW10 收口为「已落地子里程碑 + 架构边界文档化」**。详 `docs/design/p3-wasm-tier/implementation-progress.md`。
  - **PW10 后续修复轮(2026-06-17,pineapple bench spike 触发)**:① **issue #18 自然热度升层路径接通**——此前 p3 build 下编译期 `analyzeCompilability` 用临时 Bridge(`b.p3==nil`)把所有 Proto 烧成 `ReasonBackendUnsupp` 占位,运行期从不重判 → 任何 Proto 即便过 `HotEntryThreshold` 也直接 Stuck、自然热度升层形同虚设(`SetForceAllPromote(false)` 形态下白盒 `PromotionCount()==0`);`considerPromotion` 现对「占位位 + P3 已注入」子集调 `recheckCompilabilityRuntime`(原 `recheckCompilabilityForce`)清 F7 占位 + 真实后端重判(F1-F6 结构性排除保留),接通生产升层路径。② **issue #21 short-proto 守卫**(见上 P2 静态分析器条)消除 short proto 升层 wasm 反噬。**testing-only 白盒探针** `State.PromotionCount()`:auto-lifting 形态下断言「真升过」(跑前=0、跑后>0),非 wangshu_p3 build / P3 未注入恒返 0。见 [[2026-06-17-issue18-p3-autolift-fix-round]] / [[2026-06-17-issue21-short-proto-guard-round]] / [[2026-06-17-pineapple-bench-batch-wrapper-spike]]。

### P4:带 IC 反馈的投机 method JIT(+1-2 人年,流水线图「trace 收益 ~70%」)

- **JSC Baseline 风格**,per-function 模板编译;IC 反馈做类型投机(**f64 快速路径 + guard**);**deopt 简单**(函数级 **OSR exit** 回解释器);
- 继承 P3 的全部分层结构,**只换发射后端**(Wasm 发射 → 原生发射);
- **amd64 + arm64 双后端**;系统管线参考 wazero。
- **验收**:列内核负载 ≥ LuaJ-luajc 档;Wasm 层退役,**或**留作可移植中层(未移植架构、禁 exec-mmap 环境)。

### P5:trace-based JIT(+2-4 人年到可信 v1,开放式,目标 10-30x)

- **trace 录制**(从字节码)、**IR 优化**(CSE / 循环不变量外提 / 分配下沉)、**寄存器分配**、**snapshot + deopt 机器**——文档称这是「LuaJIT 的真正护城河,无处抄」。
- **启动条件**:只在 P4 的收益不够时启动。
- **验收**:列内核负载 10-30x over gopher-lua。

## 贯穿全程的不变式

- **解释器永不退役**(贯穿原则 1):是所有编译层的 deopt 着陆点与语义 oracle。
- **层间逐字节差分测试**(贯穿原则 2):直接对应 P1/P3 验收里的「差分 fuzz 逐字节一致」。
- 详见 [[design-premises]]。

---

相关:[[design-premises]] · [[value-representation]] · [[embedding-contract]] · [[project-overview]] · [[glossary]]
