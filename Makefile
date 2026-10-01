VERSION ?= dev
ARCH ?= amd64

.PHONY: build test check linux mock release verify-release clean
build:
	go build -trimpath -ldflags='-X main.version=$(VERSION)' -o bin/ask53 ./cmd/ask53
mock:
	go run ./cmd/mock-provider
test:
	go test -race -count=1 -timeout=90s ./...
check:
	go vet ./...
linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=$(ARCH) go build -trimpath -ldflags='-s -w -X main.version=$(VERSION)' -o bin/ask53-linux-$(ARCH) ./cmd/ask53
release:
	bash scripts/release.sh '$(VERSION)'
verify-release:
	bash scripts/verify-release.sh '$(VERSION)'
clean:
	rm -rf bin coverage.out
