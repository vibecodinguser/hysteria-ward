package build_test

import (
	"runtime"
	"testing"

	"github.com/vibecodinguser/hysteria-ward/internal/build"
)

// TestInfoString проверяет формат строки --version.
// Ожидаемые значения взяты из спецификации формата, а не вычислены тем же способом,
// что и в коде: это литералы.
func TestInfoString(t *testing.T) {
	cases := []struct {
		name string
		info build.Info
		want string
	}{
		{
			name: "релизная сборка с запечёнными значениями",
			info: build.Info{Version: "v0.1.0", Commit: "8b2de68", Platform: "linux/amd64"},
			want: "hysteria-ward v0.1.0 (8b2de68) linux/amd64",
		},
		{
			name: "значения по умолчанию, когда ничего не запечено",
			info: build.Info{Version: "dev", Commit: "unknown", Platform: "linux/arm64"},
			want: "hysteria-ward dev (unknown) linux/arm64",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.info.String(); got != c.want {
				t.Errorf("String() = %q, ожидалось %q", got, c.want)
			}
		})
	}
}

// TestCurrentPlatform проверяет, что платформа берётся из сборки, под которую
// собран сам тест, а не подставляется константой.
func TestCurrentPlatform(t *testing.T) {
	want := runtime.GOOS + "/" + runtime.GOARCH

	if got := build.Current().Platform; got != want {
		t.Errorf("Current().Platform = %q, ожидалось %q", got, want)
	}
}

// TestCurrentWithoutInjectedMetadata проверяет значения по умолчанию: тесты
// собираются без -ldflags, поэтому версия и коммит обязаны быть заглушками.
func TestCurrentWithoutInjectedMetadata(t *testing.T) {
	got := build.Current()

	if got.Version != "dev" {
		t.Errorf("Current().Version = %q, ожидалось %q", got.Version, "dev")
	}
	if got.Commit != "unknown" {
		t.Errorf("Current().Commit = %q, ожидалось %q", got.Commit, "unknown")
	}
}
