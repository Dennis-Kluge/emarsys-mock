BINARY   := bin/emarsys-mock
RECORDER := bin/emarsys-record
PKG      := ./cmd/emarsys-mock
REC_PKG  := ./cmd/emarsys-record

.PHONY: all build test race vet fmt check run docker smoke clean
BASE ?= http://localhost:8080

all: check build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BINARY) $(PKG)
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(RECORDER) $(REC_PKG)

# Proves the deployment promise: a static binary for another platform, no CGO.
build-linux-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" \
		-o $(BINARY)-linux-arm64 $(PKG)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" \
		-o $(RECORDER)-linux-arm64 $(REC_PKG)

test:
	go test -timeout 120s ./...

race:
	go test -race -timeout 300s ./...

vet:
	go vet ./...

fmt:
	gofmt -w cmd internal

check: vet
	@test -z "$$(gofmt -l cmd internal)" || { echo "gofmt needed:"; gofmt -l cmd internal; exit 1; }
	$(MAKE) test

run:
	go run $(PKG)

docker:
	docker build -t emarsys-mock:dev .

# Runs the definition-of-done checks against an instance you already started.
smoke:
	./scripts/smoke.sh $(BASE)

clean:
	rm -rf bin
