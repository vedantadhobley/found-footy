// Package tracing is the stub OpenTelemetry SDK integration.
//
// Real distributed-tracing wiring is deferred. This package gives adapters a
// stable import path without making them configure OpenTelemetry directly.
//
// Today: Noop returns a noop tracer that satisfies whatever interface
// future adapter code needs. When real tracing lands, this package
// swaps to configuring an OTLP exporter without changing any adapter's
// import path.
package tracing

// Tracer is the noop tracer type that adapter code will type-assert
// against once the real interface is designed. Empty for now.
type Tracer struct{}

// Noop returns a no-op Tracer suitable for use anywhere real tracing
// isn't wired yet. Safe for concurrent use.
func Noop() *Tracer {
	return &Tracer{}
}
