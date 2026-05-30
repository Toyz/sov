#!/usr/bin/env bash
# bench-guard.sh — coarse benchmark regression net for CI.
#
# Runs the Go benchmarks, takes the BEST (min) ns/op per benchmark across
# -count runs (min is the most noise-robust "did it get slower" signal),
# and fails if any benchmark exceeds its committed baseline by more than
# THRESHOLD_PCT.
#
# This is intentionally dependency-free (no benchstat) and the threshold is
# GENEROUS: microbenchmarks are noisy and CI hardware differs from wherever
# bench/baseline.txt was captured, so this catches catastrophic regressions
# (a 2x blow-up), not small drifts. Re-baseline with:
#
#   COUNT=8 scripts/bench-guard.sh --update
#
# Tune via env: THRESHOLD_PCT (default 80), COUNT (default 6).
set -euo pipefail

cd "$(dirname "$0")/.."
BASELINE="bench/baseline.txt"
THRESHOLD_PCT="${THRESHOLD_PCT:-80}"
COUNT="${COUNT:-6}"
PKGS="./rpc/ ./gateway/"

run_benches() {
  go test -bench=. -benchmem -run='^$' -count="$COUNT" $PKGS 2>/dev/null \
    | grep -E '^Benchmark' \
    | awk '{print $1, $3}' \
    | sed -E 's/-[0-9]+ / /' \
    | sort -k1,1 -k2,2n \
    | awk '!seen[$1]++ {print $1, $2}'
}

if [[ "${1:-}" == "--update" ]]; then
  mkdir -p bench
  run_benches > "$BASELINE"
  echo "updated $BASELINE:"
  cat "$BASELINE"
  exit 0
fi

if [[ ! -f "$BASELINE" ]]; then
  echo "no baseline at $BASELINE — run: scripts/bench-guard.sh --update" >&2
  exit 2
fi

current="$(run_benches)"
echo "benchmark            baseline ns/op   current ns/op   delta"
echo "-------------------------------------------------------------"
fail=0
while read -r name base; do
  cur="$(echo "$current" | awk -v n="$name" '$1==n {print $2}')"
  if [[ -z "$cur" ]]; then
    echo "$name  MISSING in current run (renamed/removed?)"
    continue
  fi
  # integer percentage delta vs baseline
  delta=$(( (cur - base) * 100 / base ))
  flag=""
  if (( delta > THRESHOLD_PCT )); then
    flag="  <-- REGRESSION (> ${THRESHOLD_PCT}%)"
    fail=1
  fi
  printf "%-20s %12s %15s   %+d%%%s\n" "$name" "$base" "$cur" "$delta" "$flag"
done < "$BASELINE"

if (( fail )); then
  echo
  echo "FAIL: a benchmark regressed beyond ${THRESHOLD_PCT}%." >&2
  echo "If intentional (or hardware changed), re-baseline: scripts/bench-guard.sh --update" >&2
  exit 1
fi
echo
echo "OK: no benchmark regressed beyond ${THRESHOLD_PCT}%."
