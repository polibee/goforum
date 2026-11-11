VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: assets build run test tidy clean

assets:
	npm install
	npm run build

build: assets
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/goforum ./cmd/goforum

run:
	go run ./cmd/goforum

test:
	go test ./...

tidy:
	go mod tidy

clean:
	rm -rf bin data
