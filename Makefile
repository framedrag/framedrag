.PHONY: build test test-race cover cover-html vet lint fmt-check tidy tidy-check vuln check clean

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.date=$(DATE)

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/framedrag ./cmd/framedrag

tidy:
	go mod tidy

tidy-check:
	go mod tidy
	@git diff --exit-code go.mod go.sum || \
		(echo "go.mod/go.sum not tidy; run 'make tidy' and commit the result" && exit 1)

test:
	go test ./...

test-race:
	go test -race ./...

cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

cover-html: cover
	go tool cover -html=coverage.out -o coverage.html

vet:
	go vet ./...

lint:
	@command -v golangci-lint >/dev/null || \
		(echo "golangci-lint not installed: https://golangci-lint.run/welcome/install/" && exit 1)
	golangci-lint run --timeout=5m

fmt-check:
	@out=$$(gofmt -l .); \
	if [ -n "$$out" ]; then \
		echo "gofmt needed on:"; echo "$$out"; exit 1; \
	fi

vuln:
	@test -x ./bin/govulncheck || GOBIN=$(PWD)/bin go install golang.org/x/vuln/cmd/govulncheck@latest
	./bin/govulncheck ./...

check: fmt-check vet test-race vuln

clean:
	rm -f bin/framedrag coverage.out coverage.html
