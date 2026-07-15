#!/usr/bin/env python3
# bench_readme_table.py <logdir>
#
# Parse the three raw benchmark logs bench-readme-table.sh drops into
# <logdir> (p1.log / p3.log / p4.log) into the Markdown text of the README
# performance table.
#
# Each benchmark takes the median (ns/op) over its -count runs; the ratio is
# gopher / X (higher is better). Output follows the README's existing layout
# conventions:
#   - format "wall time (ratio x)"
#   - **bold** = fastest wall time in the row (gopher column included)
#   - <ins>underline</ins> = ratio >= 1.5x
#   - `—` = the scenario does not apply to this column
#
# Standalone: python3 scripts/bench_readme_table.py <logdir>
# Normally invoked via scripts/bench-readme-table.sh.

import os
import re
import sys
import statistics

# -- two asymmetries in the benchmark naming map (see the *_test.go files
#    under benchmarks/):
#   1. baseline (Simple/Arith/Loop) has no separate kernel bench under the P4
#      build: baseline_test.go carries the `!wangshu_p3` tag, so the P4 build
#      compiles it too; profiling is on but the top-level chunk never
#      promotes, hence P4 auto == P4 force == the measured `_Wangshu`.
#   2. P3 force CallInto: MiniCallOnly / RealworldPredicate / Transform keep
#      the historical `_Gibbous` name (which IS the zero-alloc CallInto
#      variant); only MiniBoundary has an explicit `_GibbousCallInto`.
# If benchmark names change, update both spots here.
#
# -- baseline P3 column workload basis (issue #93) --------------------------
# The P3 baseline benches measure the wrapKernel(body)x50 shape (a vararg
# top-level chunk never promotes, so the body must be kernel-wrapped),
# which is a DIFFERENT workload from the bare top-level x1 the gopher /
# P1 / P4 columns run. The ratio denominator is therefore the same-shape
# `_GopherKernel` (baseline_test.go: gopher running the identical
# wrapKernel x50), the cell carries a [^p3-kernel] footnote, and the wall
# time is excluded from the row's "fastest" bold comparison (comparing
# wall times across shapes is meaningless). The old basis divided by the
# top-level x1 gopher number, understating P3 by ~50x.

# -- footnote markers: which cell carries which footnote (editorial mapping;
#    keep in sync when footnotes change) --
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
    """Read one log; return {benchmark name: median(ns/op)}."""
    acc = {}
    if not os.path.exists(path):
        return acc
    with open(path) as f:
        for line in f:
            m = re.match(r'^(Benchmark\S+)\s+\d+\s+([\d.]+)\s+ns/op', line)
            if not m:
                continue
            # Normalize Go's GOMAXPROCS suffix: with `-cpu=1` Go appends no
            # `-N`, but if --format-only is fed a multi-core log (names
            # carrying `-8` etc.), the suffix must be stripped for the bare
            # names in the formatter to match (otherwise the promoted-tier
            # columns silently become `—`).
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
    """Core of a wangshu cell (wall time + ratio + optional underline), without bold / footnotes."""
    txt = f'{fmt_time(ns, unit)} ({fmt_ratio(g, ns)})'
    if g / ns >= 1.5:
        txt = f'<ins>{txt}</ins>'
    return txt


def build_row(label, unit, g, cols):
    """cols = [(ns, gden, note), ...] for P1/P3a/P3f/P4a/P4f.

    gden is the cell's ratio denominator: None = same workload as the
    row's gopher (use g); non-None = the cell measures a different
    workload shape (issue #93: baseline P3 columns run kernel x50, so
    the denominator must be the same-shape _GopherKernel) — the ratio
    then uses gden and the wall time is excluded from the row's
    "fastest" bold comparison (cross-shape wall times don't compare).

    The gopher column shows wall time only, no ratio. Bold goes to the
    row's fastest wall time (gopher included); footnotes attach outside
    the bold (matching the README's existing layout: `**...** [^x]`)."""
    walls = [g] + [c[0] for c in cols if c[0] is not None and c[1] is None]
    fastest = min(walls)
    out = []
    gtxt = fmt_time(g, unit)
    out.append(f'**{gtxt}**' if g == fastest else gtxt)
    for ns, gden, note in cols:
        if ns is None:
            out.append('—')
            continue
        den = gden if gden is not None else g
        txt = cell_core(den, ns, unit)
        if gden is None and ns == fastest:
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

    # -- pure-VM micro benchmarks (baseline) --
    base = [('Simple (分支/比较)', 'Simple', 'ns'),
            ('Arith (Horner)', 'Arith', 'ns'),
            ('Loop (求和循环)', 'Loop', 'us')]
    for i, (label, key, unit) in enumerate(base):
        cat = '纯 VM 微基准 [^cat-baseline]' if i == 0 else ''
        g = p1[f'Benchmark{key}_Gopher']
        # The P3 columns' workload is wrapKernel(body)x50 (a vararg
        # top-level chunk never promotes, so P3 must measure the
        # kernel-wrapped shape); the ratio denominator is the
        # same-shape _GopherKernel (issue #93: dividing by the
        # top-level x1 g understated P3 by ~50x). When _GopherKernel is
        # missing (old logs) pass None so the cell prints `—` instead
        # of a wrong ratio.
        gk = p1.get(f'Benchmark{key}_GopherKernel')
        p3a = p3.get(f'Benchmark{key}_WangshuKernel') if gk is not None else None
        p3f = p3.get(f'Benchmark{key}_Gibbous') if gk is not None else None
        cols = [
            (p1.get(f'Benchmark{key}_Wangshu'), None, fn(key, 'p1')),
            # Concatenate rather than pick-one: a cell that ever needs
            # both a gate footnote and the kernel-shape footnote shows
            # both (PR #101 review note).
            (p3a, gk, (fn(key, 'p3a') + ' [^p3-kernel]').strip()),
            (p3f, gk, (fn(key, 'p3f') + ' [^p3-kernel]').strip()),
            (p4.get(f'Benchmark{key}_Wangshu'), None, fn(key, 'p4a')),
            (p4.get(f'Benchmark{key}_Wangshu'), None, fn(key, 'p4f')),
        ]
        lines.append(_row_with_cat(cat, label, unit, g, cols))

    # -- heavy kernels + realworld small --
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

    # -- boundary mini (Call / CallInto) --
    for variant, catname in [('Call', '边界 mini · Call [^cat-mini]'),
                             ('CallInto', '边界 mini · CallInto [^cat-mini]')]:
        # PureVM row: P1 only; promoted-tier columns are `—`
        g = p1['BenchmarkMiniPureVM_Gopher']
        pv_cols = [(p1.get('BenchmarkMiniPureVM_Wangshu'), None, ''),
                   (None, None, ''), (None, None, ''), (None, None, ''), (None, None, '')]
        lines.append(_row_with_cat(catname, 'PureVM', 'ns', g, pv_cols))
        for label, key in [('CallOnly', 'MiniCallOnly'), ('Boundary (+SetGlobal)', 'MiniBoundary')]:
            g = p1[f'Benchmark{key}_Gopher']
            lines.append(_row_with_cat('', label, 'ns', g, _emb_cols(p1, p3, p4, key, variant)))

    # -- realworld workloads (Call / CallInto) --
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
    # build_row emits `| <label> | ...`; replace the first column with `| <cat> | <label> |`
    body = row[len(f'| {label} '):]  # strip `| <label> `
    return f'| {cat} | {label} {body}'


def _hr_cols(p1, p3, p4, key, fn):
    return [
        (p1.get(f'Benchmark{key}_Wangshu'), None, fn(key, 'p1')),
        (p3.get(f'Benchmark{key}_GibbousAuto'), None, fn(key, 'p3a')),
        (p3.get(f'Benchmark{key}_Gibbous'), None, fn(key, 'p3f')),
        (p4.get(f'Benchmark{key}_GibbousJITAuto'), None, fn(key, 'p4a')),
        (p4.get(f'Benchmark{key}_GibbousJIT'), None, fn(key, 'p4f')),
    ]


def _emb_cols(p1, p3, p4, key, variant):
    # P3 force CallInto historical naming asymmetry (see header comment 2)
    if variant == 'CallInto' and key in ('MiniCallOnly', 'RealworldPredicate', 'RealworldTransform'):
        p3f = p3.get(f'Benchmark{key}_Gibbous')
    else:
        p3f = p3.get(f'Benchmark{key}_Gibbous{variant}')
    return [
        (p1.get(f'Benchmark{key}_Wangshu{variant}'), None, ''),
        (p3.get(f'Benchmark{key}_GibbousAuto{variant}'), None, ''),
        (p3f, None, ''),
        (p4.get(f'Benchmark{key}_GibbousJITAuto{variant}'), None, ''),
        (p4.get(f'Benchmark{key}_GibbousJIT{variant}'), None, ''),
    ]


if __name__ == '__main__':
    main()
