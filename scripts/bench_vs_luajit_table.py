#!/usr/bin/env python3
# bench_vs_luajit_table.py <logdir>
#
# 把 bench-vs-luajit.sh 落在 <logdir> 里的各引擎日志(p1.log / p4auto.log /
# p4force.log / luajit.log,每行 "name<TAB>iters<TAB>us_per_iter",count 轮
# 追加)解析成 Markdown 对比表。
#
# 每个 kernel 取多轮 median(us/iter);倍率列 = P1 / X(越大越好,X 相对
# 解释器的加速比)。LuaJIT 日志缺失时该列输出 n/a。
#
# 单独跑:python3 scripts/bench_vs_luajit_table.py <logdir>
# 一般经 scripts/bench-vs-luajit.sh 调用。

import os
import statistics
import sys

ENGINES = [
    ("p1", "P1 (interp)"),
    ("p4auto", "P4-auto"),
    ("p4force", "P4-force"),
    ("luajit", "LuaJIT"),
]

KERNEL_ORDER = [
    "simple", "arith", "loop",
    "heavy_arith", "heavy_floatloop", "heavy_recursion", "fib",
]


def parse(logf):
    """Return {kernel: median_us}. Missing file -> None."""
    if not os.path.isfile(logf):
        return None
    samples = {}
    with open(logf) as fh:
        for line in fh:
            parts = line.rstrip("\n").split("\t")
            if len(parts) != 3 or parts[0] == "checksum":
                continue
            name, _, us = parts
            try:
                samples.setdefault(name, []).append(float(us))
            except ValueError:
                continue
    return {k: statistics.median(v) for k, v in samples.items()}


def fmt_us(us):
    if us >= 1000:
        return "%.2f ms" % (us / 1000.0)
    if us >= 1:
        return "%.2f µs" % us
    return "%.0f ns" % (us * 1000.0)


def main():
    if len(sys.argv) != 2:
        print("usage: bench_vs_luajit_table.py <logdir>", file=sys.stderr)
        return 2
    logdir = sys.argv[1]
    data = {key: parse(os.path.join(logdir, key + ".log")) for key, _ in ENGINES}
    if data["p1"] is None:
        print("missing p1.log under %s" % logdir, file=sys.stderr)
        return 1

    header = "| kernel | " + " | ".join(label for _, label in ENGINES) + " |"
    sep = "|---" * (len(ENGINES) + 1) + "|"
    print(header)
    print(sep)
    for kernel in KERNEL_ORDER:
        base = data["p1"].get(kernel)
        cells = []
        for key, _ in ENGINES:
            eng = data[key]
            us = eng.get(kernel) if eng else None
            if us is None:
                cells.append("n/a")
            elif key == "p1":
                cells.append(fmt_us(us))
            else:
                ratio = base / us if us > 0 else float("inf")
                cells.append("%s (%.2fx)" % (fmt_us(us), ratio))
        print("| %s | %s |" % (kernel, " | ".join(cells)))
    return 0


if __name__ == "__main__":
    sys.exit(main())
