// Package observability настраивает экспорт трейсов OpenTelemetry:
// провайдер с OTLP-экспортёром, сэмплер и propagator. Пустой endpoint
// в конфиге означает выключенный трейсинг — Setup возвращает noop-провайдер,
// и весь инструментированный код работает без экспорта.
package observability

import (
	"context"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"

	"github.com/kerpe-l/gophprofile/internal/buildinfo"
	"github.com/kerpe-l/gophprofile/internal/config"
)

// Provider владеет экспортом трейсов. Нулевое значение — noop.
type Provider struct {
	tp *sdktrace.TracerProvider
}

// Setup собирает провайдер трейсов и регистрирует его глобально вместе с
// W3C-propagator'ом. Соединение с коллектором ленивое: недоступный endpoint
// не мешает старту, ошибки экспорта уходят в лог.
func Setup(ctx context.Context, cfg config.Otel, service, env string, log *slog.Logger) (*Provider, error) {
	if cfg.Endpoint == "" {
		return &Provider{}, nil
	}

	opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(cfg.Endpoint)}
	if cfg.Insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}

	exporter, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("create otlp exporter: %w", err)
	}

	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(service),
		semconv.ServiceVersion(buildinfo.Get().Version),
		semconv.DeploymentEnvironmentNameKey.String(env),
	))
	if err != nil {
		return nil, fmt.Errorf("build otel resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		log.Warn("otel export", slog.Any("error", err))
	}))

	return &Provider{tp: tp}, nil
}

// Shutdown останавливает экспорт, дождавшись отправки накопленных спанов.
// Вызывать после остановки приёма работы, но до закрытия остальных ресурсов.
func (p *Provider) Shutdown(ctx context.Context) error {
	if p.tp == nil {
		return nil
	}

	if err := p.tp.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown tracer provider: %w", err)
	}

	return nil
}
