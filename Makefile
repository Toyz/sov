# sov developer tasks. Build output goes to bin/out/ (gitignored).

.PHONY: build test bench bench-guard bench-baseline conform-py

build:
	go build ./...

test:
	go test ./...

# Print the benchmark table (see BENCHMARKS.md).
bench:
	go test -bench=. -benchmem -run='^$$' ./rpc/ ./gateway/

# Fail if any benchmark regressed past the threshold vs bench/baseline.txt.
bench-guard:
	scripts/bench-guard.sh

# Re-capture the benchmark baseline (after an intentional perf change).
bench-baseline:
	scripts/bench-guard.sh --update
