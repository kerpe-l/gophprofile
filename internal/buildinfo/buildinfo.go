// Package buildinfo хранит сведения о сборке бинарника.
package buildinfo

import "log/slog"

// Значения подставляет линковщик: `go build -ldflags "-X <путь>/internal/buildinfo.version=..."`.
// Флаг -X пишет только в строковые переменные уровня пакета.
//
//nolint:gochecknoglobals // требование -ldflags -X
var (
	version   = "dev"
	buildDate = "unknown"
)

// Build — сведения о сборке. У локальной сборки без -ldflags поля равны
// "dev" и "unknown"; Date — время в UTC.
type Build struct {
	Version string
	Date    string
}

// Get возвращает сведения о текущей сборке.
func Get() Build {
	return Build{Version: version, Date: buildDate}
}

// LogValue реализует slog.LogValuer: сборка пишется в лог группой полей.
func (b Build) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("version", b.Version),
		slog.String("date", b.Date),
	)
}
