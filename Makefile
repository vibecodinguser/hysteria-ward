MODULE  := github.com/vibecodinguser/hysteria-ward
BINARY  := hysteria-ward

# Версия и коммит запекаются линкером. Умолчания совпадают с теми, что видит
# сборка без -ldflags: "dev" и "unknown".
VERSION ?= dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)

LDFLAGS := -s -w \
	-X $(MODULE)/internal/build.version=$(VERSION) \
	-X $(MODULE)/internal/build.commit=$(COMMIT)

.PHONY: all build ui test lint clean

all: test lint build

# CGO_ENABLED=0 обязателен: бинарь должен быть статическим и не зависеть от
# библиотек хоста. GOOS и GOARCH можно переопределить снаружи для кросс-сборки.
build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY) ./cmd/$(BINARY)

# Интерфейс собирается в internal/ui/dist и оттуда вшивается в бинарь. Node.js
# нужен только здесь: результат лежит в репозитории, поэтому на сервере не
# появляется ни интерпретаторов, ни файлов рядом с бинарём.
#
# После правок в web/ этот шаг нужно повторить, иначе `make build` соберёт
# прежний интерфейс — за этим следит проверка в CI.
ui:
	cd web && npm ci && npm run build

test:
	go test ./...

lint:
	golangci-lint run ./...

clean:
	rm -rf bin
