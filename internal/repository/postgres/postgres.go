// Package postgres хранит метаданные аватаров в PostgreSQL.
//
// Каждый запрос отбрасывает мягко удалённые записи, поэтому удалённый аватар
// неотличим от несуществующего: и тот и другой дают domain.ErrNotFound.
//
// Пакет возвращает конкретный *Repository; интерфейсы нужного им размера
// объявляют потребители.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kerpe-l/gophprofile/internal/config"
	"github.com/kerpe-l/gophprofile/internal/domain"
)

// Repository — доступ к таблице аватаров.
type Repository struct {
	pool *pgxpool.Pool
	// queryTimeout — предел на один запрос. Поле, а не константа пакета,
	// чтобы тест мог подставить своё значение и не ждать реальных секунд.
	queryTimeout time.Duration
}

// New открывает пул соединений и проверяет его первым запросом к базе.
// Пул принадлежит репозиторию: закрывается методом Close и только им.
func New(ctx context.Context, cfg config.DB) (*Repository, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse database dsn: %w", err)
	}

	// Размер пула pgx хранит в int32, а конфиг читает его как int.
	if cfg.MaxConns <= 0 || cfg.MaxConns > math.MaxInt32 {
		return nil, fmt.Errorf("max connections %d is out of range", cfg.MaxConns)
	}

	poolCfg.MaxConns = int32(cfg.MaxConns)

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}

	repo := &Repository{pool: pool, queryTimeout: cfg.QueryTimeout}

	if err := repo.Ping(ctx); err != nil {
		pool.Close()

		return nil, err
	}

	return repo, nil
}

// Close закрывает пул и ждёт возврата занятых соединений.
// Повторный вызов безопасен.
func (r *Repository) Close() {
	r.pool.Close()
}

// Ping проверяет, что база отвечает.
func (r *Repository) Ping(ctx context.Context) error {
	ctx, cancel := r.withDeadline(ctx)
	defer cancel()

	if err := r.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

	return nil
}

// withDeadline — единственное место, где на обращение к базе ставится предел
// времени: его зовут все хелперы доступа. Ставится безусловно — WithTimeout
// не продлевает более ранний дедлайн вызывающего.
func (r *Repository) withDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, r.queryTimeout)
}

// exec выполняет изменяющий запрос. Запрос обязан отбирать строку по
// первичному ключу и фильтровать `deleted_at IS NULL`: ноль затронутых строк
// трактуется как отсутствие живой записи.
func (r *Repository) exec(ctx context.Context, query string, args ...any) error {
	ctx, cancel := r.withDeadline(ctx)
	defer cancel()

	tag, err := r.pool.Exec(ctx, query, args...)
	if err != nil {
		return err
	}

	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}

	return nil
}

// execTransition выполняет перевод статуса: запрос обязан отбирать строку
// не только по идентификатору, но и по списку разрешённых исходных статусов.
// Ноль затронутых строк означает либо запрещённый переход, либо отсутствие
// живой записи, и эти случаи различаются отдельно.
func (r *Repository) execTransition(ctx context.Context, id uuid.UUID, query string, args ...any) error {
	err := r.exec(ctx, query, args...)
	if !errors.Is(err, domain.ErrNotFound) {
		return err
	}

	return r.transitionError(ctx, id)
}

// transitionError называет причину, по которой перевод статуса не затронул
// ни одной строки. Второе чтение идёт только по ветке отказа и ни на что
// не влияет: решение уже принято базой, а гонка с удалением записи между
// двумя запросами меняет domain.ErrInvalidTransition на domain.ErrNotFound —
// на тот ответ, который к этому моменту и стал верным.
func (r *Repository) transitionError(ctx context.Context, id uuid.UUID) error {
	ctx, cancel := r.withDeadline(ctx)
	defer cancel()

	var alive bool

	err := r.pool.QueryRow(ctx, avatarExistsQuery, id).Scan(&alive)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return domain.ErrNotFound
	case err != nil:
		return err
	default:
		return domain.ErrInvalidTransition
	}
}

// queryOne читает ровно одну строку. Отсутствие строки — domain.ErrNotFound.
func (r *Repository) queryOne(ctx context.Context, query string, args ...any) (domain.Avatar, error) {
	ctx, cancel := r.withDeadline(ctx)
	defer cancel()

	avatar, err := scanAvatar(r.pool.QueryRow(ctx, query, args...))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Avatar{}, domain.ErrNotFound
		}

		return domain.Avatar{}, err
	}

	return avatar, nil
}

// queryMany отдаёт строки по одной, не собирая их в слайс: вызывающий
// прерывает перебор на первой же ошибке, не вычитав остальное.
// what попадает в текст ошибки и называет объект запроса.
func (r *Repository) queryMany(ctx context.Context, what, query string, args ...any) iter.Seq2[domain.Avatar, error] {
	return func(yield func(domain.Avatar, error) bool) {
		ctx, cancel := r.withDeadline(ctx)
		defer cancel()

		rows, err := r.pool.Query(ctx, query, args...)
		if err != nil {
			yield(domain.Avatar{}, fmt.Errorf("%s: %w", what, err))

			return
		}
		defer rows.Close()

		for rows.Next() {
			avatar, err := scanAvatar(rows)
			if err != nil {
				yield(domain.Avatar{}, fmt.Errorf("%s: %w", what, err))

				return
			}

			if !yield(avatar, nil) {
				return
			}
		}

		if err := rows.Err(); err != nil {
			yield(domain.Avatar{}, fmt.Errorf("%s: %w", what, err))
		}
	}
}

// failedSeq — последовательность из одной ошибки. Аргументы перебора
// проверяются до запроса, а сообщить о них вызывающему можно только
// через сам перебор.
func failedSeq(err error) iter.Seq2[domain.Avatar, error] {
	return func(yield func(domain.Avatar, error) bool) {
		yield(domain.Avatar{}, err)
	}
}
