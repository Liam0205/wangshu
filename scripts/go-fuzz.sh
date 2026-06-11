#!/usr/bin/env bash
# go-fuzz.sh <fuzztime>:自动发现全部 func Fuzz* 目标,各跑一轮冒烟。engineering.md §1。
set -euo pipefail

fuzztime="${1:-30s}"
found=0

while IFS=: read -r file line decl; do
    func=$(echo "$decl" | sed -E 's/^func (Fuzz[A-Za-z0-9_]*).*/\1/')
    pkg=$(dirname "$file")
    found=1
    echo "fuzz: $pkg :: $func ($fuzztime)"
    go test "./$pkg" -run='^$' -fuzz="^${func}\$" -fuzztime="$fuzztime" -timeout=120s -parallel=4
done < <(grep -rn --include='*_test.go' -E '^func Fuzz[A-Za-z0-9_]*\(f \*testing\.F\)' . 2>/dev/null || true)

if [ "$found" -eq 0 ]; then
    echo "no fuzz targets found (func Fuzz*),skip"
fi
