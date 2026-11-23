VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test run dist clean
build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o naavd .
test:
	go vet ./... && go test ./...
run: build
	./naavd
dist:
	mkdir -p dist
	for arch in arm64 amd64; do \
	  CGO_ENABLED=0 GOOS=darwin GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o dist/naavd . && \
	  tar -C dist -czf dist/naavd_$(VERSION)_darwin_$$arch.tar.gz naavd && rm dist/naavd; \
	done
	cd dist && shasum -a 256 *.tar.gz > checksums.txt
clean:
	rm -rf naavd dist
