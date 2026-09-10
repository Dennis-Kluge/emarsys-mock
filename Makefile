BINARY := bin/emarsys-mock
PKG    := ./cmd/emarsys-mock

.PHONY: all build test vet fmt check run docker clean

all: check build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BINARY) $(PKG)

# Proves the deployment promise: a static binary for another platform, no CGO.
build-linux-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" \
		-o $(BINARY)-linux-arm64 $(PKG)

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

clean:
	rm -rf bin
