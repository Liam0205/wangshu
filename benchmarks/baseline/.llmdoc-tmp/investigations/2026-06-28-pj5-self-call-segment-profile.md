# PJ5 SELF + CALL spec template CALL 段瓶颈 profile(2026-06-28)

## 背景

PJ5 SELF + CALL spec template 真接入后,benchmark P4 ratio 1.12x(比 crescent 慢 12%)。
§9.19 诚实结论:SELF 段已字节级 inline,CALL 段仍走 host.CallBaseline 是瓶颈。
本调查 profile 确认 CALL 段瓶颈的真实构成。

## profile(BenchmarkGibbousJIT_PJ5SelfCallSpec,Xeon 6982P,2s)

```
executeLoop      95% cum  ← caller 主循环 + 被调 method 体执行
doCall           74% cum
enterGibbous     71% cum  ← method 体升层后跑 P4(本 bench method 体也 force-all 升了)
enterLuaFrame    30% cum  ← 帧建立
popCallInfo       6% cum  ← 帧拆除
```

## 关键发现

1. **本 bench 的 method 体 `function(self) count=count+1 end` 也被 force-all 升层**
   → 95% executeLoop 含 method 体执行 + 帧建拆,**不是 SELF 段或 CALL dispatch 的开销**。

2. **CALL 段的"瓶颈"是被调函数体的真实执行 + 帧建拆(enterLuaFrame 30% + popCallInfo 6%)**
   —— 这是不可消除的架构成本(method 体必须执行,帧必须建拆)。

3. **P4 vs crescent 慢 12% 的本质**:P4 caller 帧经 enterGibbous + runSpecSelfCall +
   host.CallBaseline round-trip;crescent caller 直接在 executeLoop 里 doCall。
   P4 多了 enterGibbous trampoline + host 边界一层。

4. **与 PW10 call 核 0.52x 同源**:P3 PW10 实测 call 核退化根因是帧建立 + 重入
   (memory project_pw10_r3_mechanism),不是 dispatch。P4 SELF + CALL 同理——
   升层一个只含单次 method call 的小函数,trampoline 开销 > 省下的 dispatch。

## 结论

CALL 段字节级 inline 在 P4 的真实收益**受限于「帧建立 + method 体执行」架构成本**。
spec template 已把能 inline 的 SELF 段 inline 了(省 host.Self round-trip,1.19x→1.12x)。
要进一步消除 CALL 段开销需要「帧建立内联」(等价 PW10 Option B:Proto 元数据 +
线程热态全迁,帧建立也内联),工程量极大、ROI 受架构成本限制。

**真正能让 SELF + CALL 形态超 crescent 的场景**:method 体计算密集(非 count++ 这种
单 op)时,P4 升层 method 体本身的加速(如 method 体含 FORLOOP / 算术链)会主导,
caller 的 SELF+CALL trampoline 开销被摊薄。本 bench 的 method 体过简(单 ADD),
放大了 trampoline 占比 —— 是 bench 形态问题,非纯架构税。

## 后续(可选,ROI 受限)

- 计算密集 method 体的 SELF + CALL bench(验摊薄效应)
- 帧建立内联(等价 PW10 Option B,极大工程,ROI 受架构成本限制)
