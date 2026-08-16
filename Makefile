# sov developer tasks. Build output goes to bin/out/ (gitignored).

.PHONY: build test bench conform-py

build:
	go build ./...

test:
	go test ./...

# Print the benchmark table (see BENCHMARKS.md).
bench:
	go test -bench=. -benchmem -run='^$$' ./rpc/ ./gateway/
