// Package migrations содержит SQL-миграции схемы и встраивает их в бинарник,
// чтобы мигратору не требовался смонтированный каталог с файлами.
package migrations

import "embed"

// FS — встроенные файлы миграций в формате goose.
//
//go:embed *.sql
var FS embed.FS
