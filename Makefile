MODULE  := github.com/vibecodinguser/hysteria-ward
BINARY  := hysteria-ward

# Версия и коммит запекаются линкером. Умолчания совпадают с теми, что видит
# сборка без -ldflags: "dev" и "unknown".
VERSION ?= dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)

LDFLAGS := -s -w \
	-X $(MODULE)/internal/build.version=$(VERSION) \
	-X $(MODULE)/internal/build.commit=$(COMMIT)

.PHONY: all build test lint clean

all: test lint build

# CGO_ENABLED=0 обязателен: бинарь должен быть статическим и не зависеть от
# библиотек хоста. GOOS и GOARCH можно переопределить снаружи для кросс-сборки.
build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY) ./cmd/$(BINARY)

test:
	go test ./...

lint:
	golangci-lint run ./...

clean:
	rm -rf bin
