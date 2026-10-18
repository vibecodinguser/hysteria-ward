// Package build сообщает, с какой версией и под какую платформу собран бинарь.
package build

import (
	"fmt"
	"runtime"
)

// Name — имя исполняемого файла. Оно же используется как имя systemd-юнита
// и каталогов в /etc, /var/lib и /var/log.
const Name = "hysteria-ward"

// Info — метаданные сборки.
type Info struct {
	Version  string // версия релиза; "dev", пока не запечена линкером
	Commit   string // хеш коммита; "unknown", пока не запечён
	Platform string // GOOS/GOARCH, например "linux/amd64"
}

// String возвращает однострочное представление метаданных — то, что печатает --version.
func (i Info) String() string {
	return fmt.Sprintf("%s %s (%s) %s", Name, i.Version, i.Commit, i.Platform)
}

// Значения подставляются линкером при релизной сборке:
//
//	-ldflags "-X .../internal/build.version=$(VERSION) -X .../internal/build.commit=$(COMMIT)"
var (
	version = "dev"
	commit  = "unknown"
)

// Current возвращает метаданные той сборки, в которой выполняется код.
func Current() Info {
	return Info{
		Version:  version,
		Commit:   commit,
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
	}
}
