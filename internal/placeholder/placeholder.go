// Package placeholder отдаёт стандартную аватарку — её получает клиент,
// когда у пользователя нет своей.
//
// Изображение встроено в бинарник, а не лежит в объектном хранилище: тогда
// подстановка заглушки работает и при недоступном хранилище.
package placeholder

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"io"
)

// ContentType — тип содержимого заглушки.
const ContentType = "image/png"

// etagLen — сколько шестнадцатеричных знаков хеша уходит в ETag: 64 бит
// хватает, чтобы не совпасть с ETag хранилища.
const etagLen = 16

//go:embed default.png
var defaultPNG []byte

// Placeholder — заглушка вместе с предвычисленными заголовками ответа.
type Placeholder struct {
	data []byte
	etag string
}

// New готовит заглушку к раздаче: считает ETag встроенного изображения.
func New() *Placeholder {
	sum := sha256.Sum256(defaultPNG)

	return &Placeholder{
		data: defaultPNG,
		etag: hex.EncodeToString(sum[:])[:etagLen],
	}
}

// Reader возвращает нового читателя содержимого заглушки.
func (p *Placeholder) Reader() io.Reader {
	return bytes.NewReader(p.data)
}

// ETag возвращает валидатор кеша заглушки. Значение стабильно между
// рестартами: содержимое не меняется без пересборки.
func (p *Placeholder) ETag() string {
	return p.etag
}

// Size возвращает размер заглушки в байтах.
func (p *Placeholder) Size() int64 {
	return int64(len(p.data))
}
