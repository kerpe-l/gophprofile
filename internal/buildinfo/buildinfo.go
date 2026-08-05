// Package buildinfo хранит сведения о сборке бинарника.
package buildinfo

import "log/slog"

// Значения подставляет линковщик: `go build -ldflags "-X <путь>/internal/buildinfo.version=..."`.
// Флаг -X умеет писать только в строковые переменные уровня пакета,
// поэтому обойтись константами или полями структуры здесь нельзя.
//
//nolint:gochecknoglobals // требование -ldflags -X
var (
	version   = "dev"
	buildDate = "unknown"
)

// Build — сведения о сборке.
type Build struct {
	// Version — версия, переданная линковщиком; "dev" у локальной сборки.
	Version string
	// Date — время сборки в UTC; "unknown" у локальной сборки.
	Date string
}

// Get возвращает сведения о текущей сборке.
func Get() Build {
	return Build{Version: version, Date: buildDate}
}

// LogValue реализует slog.LogValuer: сведения о сборке пишутся в лог
// группой полей, а не склеенной строкой.
func (b Build) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("version", b.Version),
		slog.String("date", b.Date),
	)
}
