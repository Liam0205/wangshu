#!/usr/bin/env python3
# bench_readme_table.py <logdir>
#
# 把 bench-readme-table.sh 落在 <logdir> 里的三份原始 benchmark 日志
# (p1.log / p3.log / p4.log)解析成 README「性能指标」表格的 Markdown 文本。
#
# 每个 benchmark 取 -count 多次的 median(ns/op),倍率 = gopher / X(越大越好)。
# 输出遵循 README 既有排版约定:
#   - 格式「wall time (倍率×)」
#   - **粗体** = 该行 wall time 最快(含 gopher 列)
#   - <ins>下划线</ins> = 倍率 >= 1.5×
#   - `—` = 该场景不涉及此列
#
# 单独跑:python3 scripts/bench_readme_table.py <logdir>
# 一般经 scripts/bench-readme-table.sh 调用。

import os
import re
import sys
import statistics

# ── benchmark 命名映射的两处不对称(见 benchmarks/ 各 *_test.go)──────────
#   1. baseline(Simple/Arith/Loop)在 P4 build 下没有独立 kernel bench:
#      baseline_test.go 是 `!wangshu_p3` tag,P4 build 也会编译,profiling
#      开着但顶层 chunk 不升层,所以 P4 auto == P4 force == `_Wangshu` 实测。
#   2. P3 force 的 CallInto:MiniCallOnly / RealworldPredicate / Transform
#      沿用历史名 `_Gibbous`(就是 CallInto 零分配变体);MiniBoundary 才有
#      显式 `_GibbousCallInto`。
# 改了 benchmark 命名后,这两处要跟着改。

# ── 脚注标记:哪个 cell 挂哪个脚注(编辑口径,改脚注时同步这里)────────────
# key = (row_key, column)  column ∈ {p3a, p3f, p4a, p4f}
FOOTNOTES = {
    ('Fib', 'p3a'): '[^p3-gate]',
    ('BinaryTrees', 'p3a'): '[^p3-gate]',
    ('SpectralNorm', 'p3a'): '[^p3-gate]',
    ('NBody', 'p3a'): '[^p3-gate]',
    ('Fib', 'p4a'): '[^seg2seg]',
    ('Fib', 'p4f'): '[^seg2seg]',
    ('BinaryTrees', 'p4f'): '[^seg2seg]',
    ('SpectralNorm', 'p4f'): '[^seg2seg]',
    ('Fannkuch', 'p4f'): '[^seg2seg]',
    ('NBody', 'p4a'): '[^math-intrinsic]',
    ('NBody', 'p4f'): '[^math-intrinsic]',
}


def parse(path):
    """读一份日志,返回 {benchmark 名: median(ns/op)}。"""
    acc = {}
    if not os.path.exists(path):
        return acc
    with open(path) as f:
        for line in f:
            m = re.match(r'^(Benchmark\S+)\s+\d+\s+([\d.]+)\s+ns/op', line)
            if not m:
                continue
            # 归一化 Go 的 GOMAXPROCS 后缀:`-cpu=1` 时 Go 不追加 `-N`,但若
            # --format-only 喂进多核跑的日志(名字带 `-8` 之类),去掉后缀才
            # 能和 formatter 里的裸名直查对上(否则升层列静默变 `—`)。
            name = re.sub(r'-\d+$', '', m.group(1))
            acc.setdefault(name, []).append(float(m.group(2)))
    return {k: statistics.median(v) for k, v in acc.items()}


def fmt_time(ns, unit):
    if ns is None:
        return None
    v = {'ns': ns, 'us': ns / 1e3, 'ms': ns / 1e6}[unit]
    if v >= 100:
        s = f'{v:.0f}'
    elif v >= 10:
        s = f'{v:.1f}'
    else:
        s = f'{v:.2f}'
    suffix = {'ns': 'ns', 'us': 'µs', 'ms': 'ms'}[unit]
    return f'{s} {suffix}'


def fmt_ratio(g, x):
    r = g / x
    return f'{r:.1f}×' if r >= 10 else f'{r:.2f}×'


def cell_core(g, ns, unit):
    """wangshu cell 的核心(wall time + 倍率 + 可选下划线),不含粗体 / 脚注。"""
    txt = f'{fmt_time(ns, unit)} ({fmt_ratio(g, ns)})'
    if g / ns >= 1.5:
        txt = f'<ins>{txt}</ins>'
    return txt


def build_row(label, unit, g, cols):
    """cols = [(ns, is_wangshu_cell, note), ...] 对应 P1/P3a/P3f/P4a/P4f。
    gopher 列单列 wall time,不带倍率。粗体给全行 wall time 最快者(含 gopher);
    脚注挂在粗体外侧(遵循 README 既有排版:`**...**  [^x]`)。"""
    walls = [g] + [c[0] for c in cols if c[0] is not None]
    fastest = min(walls)
    out = []
    gtxt = fmt_time(g, unit)
    out.append(f'**{gtxt}**' if g == fastest else gtxt)
    for ns, _is_w, note in cols:
        if ns is None:
            out.append('—')
            continue
        txt = cell_core(g, ns, unit)
        if ns == fastest:
            txt = f'**{txt}**'
        if note:
            txt = f'{txt} {note}'
        out.append(txt)
    return f'| {label} | ' + ' | '.join(out) + ' |'


def main():
    if len(sys.argv) < 2:
        print('usage: bench_readme_table.py <logdir>', file=sys.stderr)
        sys.exit(2)
    logdir = sys.argv[1]
    p1 = parse(os.path.join(logdir, 'p1.log'))
    p3 = parse(os.path.join(logdir, 'p3.log'))
    p4 = parse(os.path.join(logdir, 'p4.log'))

    def fn(row, col):
        return FOOTNOTES.get((row, col), '')

    lines = []
    lines.append('| 类别 | 脚本 | gopher | P1 | P3 auto | P3 force | P4 auto | P4 force |')
    lines.append('| --- | --- | --- | --- | --- | --- | --- | --- |')

    # ── 纯 VM 微基准(baseline)────────────────────────────────────────────
    base = [('Simple (分支/比较)', 'Simple', 'ns'),
            ('Arith (Horner)', 'Arith', 'ns'),
            ('Loop (求和循环)', 'Loop', 'us')]
    for i, (label, key, unit) in enumerate(base):
        cat = '纯 VM 微基准 [^cat-baseline]' if i == 0 else ''
        g = p1[f'Benchmark{key}_Gopher']
        cols = [
            (p1.get(f'Benchmark{key}_Wangshu'), True, fn(key, 'p1')),
            (p3.get(f'Benchmark{key}_WangshuKernel'), True, fn(key, 'p3a')),
            (p3.get(f'Benchmark{key}_Gibbous'), True, fn(key, 'p3f')),
            (p4.get(f'Benchmark{key}_Wangshu'), True, fn(key, 'p4a')),
            (p4.get(f'Benchmark{key}_Wangshu'), True, fn(key, 'p4f')),
        ]
        lines.append(_row_with_cat(cat, label, unit, g, cols))

    # ── heavy 内核 + realworld small ──────────────────────────────────────
    heavy = [('HeavyArith', 'HeavyArith', 'ms'), ('HeavyRecursion', 'HeavyRecursion', 'ms'),
             ('HeavyFloatloop', 'HeavyFloatloop', 'ms')]
    for i, (label, key, unit) in enumerate(heavy):
        cat = 'heavy 内核 [^cat-heavy]' if i == 0 else ''
        g = p1[f'Benchmark{key}_Gopher']
        cols = _hr_cols(p1, p3, p4, key, fn)
        lines.append(_row_with_cat(cat, label, unit, g, cols))

    real = [('fib', 'Fib', 'ms'), ('binary-trees', 'BinaryTrees', 'ms'),
            ('spectral-norm', 'SpectralNorm', 'ms'), ('fannkuch', 'Fannkuch', 'ms'),
            ('n-body', 'NBody', 'ms')]
    for i, (label, key, unit) in enumerate(real):
        cat = 'realworld small [^cat-realworld]' if i == 0 else ''
        g = p1[f'Benchmark{key}_Gopher']
        cols = _hr_cols(p1, p3, p4, key, fn)
        lines.append(_row_with_cat(cat, label, unit, g, cols))

    # ── 边界 mini(Call / CallInto)────────────────────────────────────────
    for variant, catname in [('Call', '边界 mini · Call [^cat-mini]'),
                             ('CallInto', '边界 mini · CallInto [^cat-mini]')]:
        # PureVM 行:只有 P1,升层列 —
        g = p1['BenchmarkMiniPureVM_Gopher']
        pv_cols = [(p1.get('BenchmarkMiniPureVM_Wangshu'), True, ''),
                   (None, True, ''), (None, True, ''), (None, True, ''), (None, True, '')]
        lines.append(_row_with_cat(catname, 'PureVM', 'ns', g, pv_cols))
        for label, key in [('CallOnly', 'MiniCallOnly'), ('Boundary (+SetGlobal)', 'MiniBoundary')]:
            g = p1[f'Benchmark{key}_Gopher']
            lines.append(_row_with_cat('', label, 'ns', g, _emb_cols(p1, p3, p4, key, variant)))

    # ── 真实负载(Call / CallInto)─────────────────────────────────────────
    for variant, catname in [('Call', '真实负载 · Call [^cat-embed]'),
                             ('CallInto', '真实负载 · CallInto [^cat-embed]')]:
        first = True
        for label, key in [('Predicate (×1000)', 'RealworldPredicate'),
                           ('Transform (×1000)', 'RealworldTransform')]:
            g = p1[f'Benchmark{key}_Gopher']
            cat = catname if first else ''
            first = False
            lines.append(_row_with_cat(cat, label, 'us', g, _emb_cols(p1, p3, p4, key, variant)))

    print('\n'.join(lines))


def _row_with_cat(cat, label, unit, g, cols):
    row = build_row(label, unit, g, cols)
    # build_row 产出 `| <label> | ...`,把首列换成 `| <cat> | <label> |`
    body = row[len(f'| {label} '):]  # 去掉 `| <label> `
    return f'| {cat} | {label} {body}'


def _hr_cols(p1, p3, p4, key, fn):
    return [
        (p1.get(f'Benchmark{key}_Wangshu'), True, fn(key, 'p1')),
        (p3.get(f'Benchmark{key}_GibbousAuto'), True, fn(key, 'p3a')),
        (p3.get(f'Benchmark{key}_Gibbous'), True, fn(key, 'p3f')),
        (p4.get(f'Benchmark{key}_GibbousJITAuto'), True, fn(key, 'p4a')),
        (p4.get(f'Benchmark{key}_GibbousJIT'), True, fn(key, 'p4f')),
    ]


def _emb_cols(p1, p3, p4, key, variant):
    # P3 force CallInto 的历史命名不对称(见文件头注释 2)
    if variant == 'CallInto' and key in ('MiniCallOnly', 'RealworldPredicate', 'RealworldTransform'):
        p3f = p3.get(f'Benchmark{key}_Gibbous')
    else:
        p3f = p3.get(f'Benchmark{key}_Gibbous{variant}')
    return [
        (p1.get(f'Benchmark{key}_Wangshu{variant}'), True, ''),
        (p3.get(f'Benchmark{key}_GibbousAuto{variant}'), True, ''),
        (p3f, True, ''),
        (p4.get(f'Benchmark{key}_GibbousJITAuto{variant}'), True, ''),
        (p4.get(f'Benchmark{key}_GibbousJIT{variant}'), True, ''),
    ]


if __name__ == '__main__':
    main()
