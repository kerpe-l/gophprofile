package imageproc

import (
	"fmt"
	"io"
)

// Validate проверяет загружаемое изображение и возвращает его тип и размеры.
//
// Порядок проверок: тип по сигнатуре файла, затем размеры из заголовка —
// изображение не декодируется целиком ни при каком исходе. Неподдерживаемый
// формат, битый или отсутствующий заголовок и анимированный webp дают
// domain.ErrUnsupportedFormat, превышение лимита пикселей —
// domain.ErrImageTooBig.
func (p *Processor) Validate(rs io.ReadSeeker) (info Info, err error) {
	// Перемотка в defer, а не в каждой ветке: инвариант «поток отдан в начале»
	// должен держаться и на ошибочных путях, которых здесь три.
	defer func() {
		if _, seekErr := rs.Seek(0, io.SeekStart); seekErr != nil && err == nil {
			info, err = Info{}, fmt.Errorf("rewind image: %w", seekErr)
		}
	}()

	head, err := readHead(rs)
	if err != nil {
		return Info{}, err
	}

	mime, err := checkMime(head)
	if err != nil {
		return Info{}, err
	}

	if _, err = rs.Seek(0, io.SeekStart); err != nil {
		return Info{}, fmt.Errorf("rewind image: %w", err)
	}

	cfg, err := p.decodeConfig(rs)
	if err != nil {
		return Info{}, err
	}

	return Info{MimeType: mime, Width: cfg.Width, Height: cfg.Height}, nil
}
