.PHONY: build run test fmt vet check clean

build:
	go build -o slopgate ./cmd/slopgate

run: build
	./slopgate

test:
	go test ./internal/... ./cmd/...

fmt:
	gofmt -l -w $$(git ls-files '*.go')

vet:
	go vet ./...

check: fmt vet build test

clean:
	rm -f slopgate
