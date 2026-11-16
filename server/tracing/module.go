// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package tracing owns the node's opt-in OpenTelemetry tracer provider and
// its OTLP/HTTP exporter. Consumers receive narrow ports from composition; no
// application use case depends on this package or on OpenTelemetry types, and
// the package never installs a process-global tracer provider.
//
// Span names and attributes come only from route templates, closed store
// operation names, registered Job types, bounded methods and outcomes, and
// server-generated request IDs. Credentials, User data, exam content, raw
// paths, query strings, SQL, and error text never enter a span.
package tracing

import (
	"context"
	"fmt"
	"log"
	"maps"
	"net/http"
	"strconv"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store/timerlayer"
)

// instrumentationName identifies Proctor's own instrumentation scope.
const instrumentationName = "github.com/sudosylabs/proctor/server"

// Identity names this node's service resource.
type Identity struct {
	Version string
	NodeID  string
}

// Module is the node's tracing lifecycle owner. A disabled Module starts no
// exporter and hands out spans that record nothing.
type Module struct {
	settings config.Tracing
	provider *sdktrace.TracerProvider
	tracer   trace.Tracer
	// propagator continues only W3C trace context; baggage is never accepted
	// because it can carry arbitrary caller-supplied content.
	propagator propagation.TraceContext

	closeOnce sync.Once
	closeErr  error
}

// New constructs the tracing module. When tracing is disabled no exporter,
// goroutine, or connection is created. errorLog receives exporter failures.
func New(settings config.Tracing, identity Identity, errorLog *log.Logger) (*Module, error) {
	if !settings.Enabled {
		return &Module{settings: settings, tracer: noop.NewTracerProvider().Tracer(instrumentationName)}, nil
	}
	exporter, err := otlptracehttp.New(
		context.Background(),
		otlptracehttp.WithEndpointURL(settings.EndpointURL),
		// Always set, even when empty, so configuration is the only header
		// source: the SDK would otherwise read OTEL_EXPORTER_OTLP_HEADERS.
		// Header values are credentials and never enter a log or span.
		otlptracehttp.WithHeaders(maps.Clone(settings.Headers)),
		otlptracehttp.WithTimeout(settings.ExportTimeout.Duration),
	)
	if err != nil {
		return nil, fmt.Errorf("construct OTLP/HTTP trace exporter: %w", err)
	}
	if errorLog != nil {
		// The SDK reports export failures through the OpenTelemetry error
		// handler; route them to the operational log instead of stderr.
		otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
			errorLog.Printf("trace export failed: %v", err)
		}))
	}
	return newModule(settings, identity, sdktrace.NewBatchSpanProcessor(exporter)), nil
}

func newModule(settings config.Tracing, identity Identity, processor sdktrace.SpanProcessor) *Module {
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(resource.NewSchemaless(
			attribute.String("service.name", "proctor"),
			attribute.String("service.version", identity.Version),
			attribute.String("service.instance.id", identity.NodeID),
		)),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(settings.SampleRatio))),
		sdktrace.WithSpanProcessor(processor),
	)
	return &Module{settings: settings, provider: provider, tracer: provider.Tracer(instrumentationName)}
}

// Enabled reports whether spans are recorded and exported.
func (m *Module) Enabled() bool { return m != nil && m.provider != nil }

// Close flushes buffered spans and stops the exporter. The flush ends when ctx,
// the caller's shutdown budget, ends or after the configured shutdown timeout,
// whichever comes first. It is idempotent and safe on a disabled module.
func (m *Module) Close(ctx context.Context) error {
	if m == nil || m.provider == nil {
		return nil
	}
	m.closeOnce.Do(func() {
		shutdownCtx, cancel := context.WithTimeout(ctx, m.settings.ShutdownTimeout.Duration)
		defer cancel()
		if err := m.provider.Shutdown(shutdownCtx); err != nil {
			m.closeErr = fmt.Errorf("shut down tracer provider: %w", err)
		}
	})
	return m.closeErr
}

// HTTPRequest is one inbound request's server span.
type HTTPRequest struct {
	span   trace.Span
	method string
	once   sync.Once
}

// StartRequest begins the server span for an inbound request, for the HTTP
// transport. Its W3C trace context is continued only when ContinueParent is
// true, which the transport sets solely for a trusted proxy peer; an
// untrusted client can neither choose the trace identity nor force sampling.
// Method must already be bounded; RequestID is the server-assigned
// correlation ID.
func (m *Module) StartRequest(ctx context.Context, request route.TracedRequest) (context.Context, route.RequestSpan) {
	method, requestID := request.Method, request.RequestID
	if request.ContinueParent && m.Enabled() {
		ctx = m.propagator.Extract(ctx, propagation.HeaderCarrier(request.Header))
	} else {
		// Discard any span the caller's context already carries so an
		// untrusted request always starts a new root trace.
		ctx = trace.ContextWithSpanContext(ctx, trace.SpanContext{})
	}
	attributes := []attribute.KeyValue{attribute.String("http.request.method", method)}
	if requestID != "" {
		attributes = append(attributes, attribute.String("proctor.request_id", requestID))
	}
	ctx, span := m.tracer.Start(ctx, method, trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(attributes...))
	return ctx, &HTTPRequest{span: span, method: method}
}

// SetRoute names the span by its sealed route template, never the raw path.
func (r *HTTPRequest) SetRoute(route string) {
	if r == nil || route == "" {
		return
	}
	r.span.SetName(r.method + " " + route)
	r.span.SetAttributes(attribute.String("http.route", route))
}

// End records the response status and ends the span once; later calls,
// such as after a protocol upgrade already ended it, are ignored.
func (r *HTTPRequest) End(status int) {
	if r == nil {
		return
	}
	r.once.Do(func() {
		r.span.SetAttributes(attribute.Int("http.response.status_code", status))
		if status >= http.StatusInternalServerError {
			r.span.SetStatus(codes.Error, strconv.Itoa(status))
		}
		r.span.End()
	})
}

// StartOperation begins an internal span for one store operation, for the
// store timing layer. It records only when the context already carries a
// recording span, so background polling never creates root traces. The
// span carries only the closed aggregate.operation name; arguments, SQL and
// rows never reach it.
func (m *Module) StartOperation(ctx context.Context, operation timerlayer.Operation) (context.Context, func(timerlayer.Outcome)) {
	if !trace.SpanFromContext(ctx).IsRecording() {
		return ctx, func(timerlayer.Outcome) {}
	}
	name := operation.String()
	ctx, span := m.tracer.Start(ctx, "store "+name, trace.WithSpanKind(trace.SpanKindInternal), trace.WithAttributes(
		attribute.String("db.system.name", "postgresql"),
		attribute.String("db.operation.name", name),
	))
	return ctx, func(outcome timerlayer.Outcome) {
		if outcome != timerlayer.OutcomeSuccess {
			span.SetStatus(codes.Error, "store operation failed")
		}
		span.End()
	}
}

// StartExecution begins the root span of one claimed Job execution, for the
// Job engine. Only the registered Job type and the closed outcome are
// recorded, never payloads or identifiers. A canceled, relinquished or
// shut-down execution is not a failure: the Job runs again elsewhere.
func (m *Module) StartExecution(ctx context.Context, jobType model.JobType) (context.Context, func(job.ExecutionOutcome)) {
	ctx, span := m.tracer.Start(
		trace.ContextWithSpanContext(ctx, trace.SpanContext{}),
		"job "+string(jobType),
		trace.WithNewRoot(),
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(attribute.String("proctor.job.type", string(jobType))),
	)
	return ctx, func(outcome job.ExecutionOutcome) {
		span.SetAttributes(attribute.String("proctor.job.outcome", string(outcome)))
		switch outcome {
		case job.ExecutionSucceeded, job.ExecutionCanceled, job.ExecutionRelinquished, job.ExecutionShutdown:
		default:
			span.SetStatus(codes.Error, string(outcome))
		}
		span.End()
	}
}

var (
	_ route.Tracing     = (*Module)(nil)
	_ timerlayer.Tracer = (*Module)(nil)
	_ job.Tracer        = (*Module)(nil)
)

// Correlation returns the trace and span IDs of the span ctx carries, for
// log correlation. ok is false when ctx carries no valid span.
func Correlation(ctx context.Context) (traceID, spanID string, ok bool) {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return "", "", false
	}
	return spanContext.TraceID().String(), spanContext.SpanID().String(), true
}
