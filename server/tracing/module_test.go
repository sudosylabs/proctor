// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package tracing

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
	"github.com/sudosylabs/proctor/server/store/timerlayer"
)

const parentTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"

func recordingModule(t *testing.T, ratio float64) (*Module, *tracetest.SpanRecorder) {
	t.Helper()
	settings := config.Default().Tracing
	settings.Enabled = true
	settings.SampleRatio = ratio
	recorder := tracetest.NewSpanRecorder()
	module := newModule(settings, Identity{Version: "test", NodeID: "node-a"}, recorder)
	t.Cleanup(func() { require.NoError(t, module.Close(context.Background())) })
	return module, recorder
}

func parentHeader() http.Header {
	header := http.Header{}
	header.Set("traceparent", "00-"+parentTraceID+"-00f067aa0ba902b7-01")
	header.Set("baggage", "student=secret")
	return header
}

func attributes(span sdktrace.ReadOnlySpan) map[attribute.Key]attribute.Value {
	values := map[attribute.Key]attribute.Value{}
	for _, value := range span.Attributes() {
		values[value.Key] = value.Value
	}
	return values
}

func TestHTTPRequestSpanIsNamedByRouteTemplateAndBoundedAttributes(t *testing.T) {
	t.Parallel()

	module, recorder := recordingModule(t, 1)
	_, span := module.StartRequest(context.Background(), route.TracedRequest{Header: http.Header{}, Method: http.MethodGet, RequestID: "request-1"})
	span.SetRoute("/api/v1/users/{user_id}")
	span.End(http.StatusServiceUnavailable)
	span.End(http.StatusOK)

	ended := recorder.Ended()
	require.Len(t, ended, 1, "End is idempotent")
	require.Equal(t, "GET /api/v1/users/{user_id}", ended[0].Name())
	values := attributes(ended[0])
	require.Equal(t, map[attribute.Key]attribute.Value{
		"http.request.method":       attribute.StringValue(http.MethodGet),
		"http.route":                attribute.StringValue("/api/v1/users/{user_id}"),
		"http.response.status_code": attribute.IntValue(http.StatusServiceUnavailable),
		"proctor.request_id":        attribute.StringValue("request-1"),
	}, values)
	require.Equal(t, codes.Error, ended[0].Status().Code)

	resource := map[attribute.Key]string{}
	for _, value := range ended[0].Resource().Attributes() {
		resource[value.Key] = value.Value.String()
	}
	require.Equal(t, map[attribute.Key]string{
		"service.name": "proctor", "service.version": "test", "service.instance.id": "node-a",
	}, resource)
}

func TestHTTPRequestContinuesTraceContextOnlyFromTrustedPeers(t *testing.T) {
	t.Parallel()

	t.Run("trusted proxy", func(t *testing.T) {
		t.Parallel()
		module, recorder := recordingModule(t, 0)
		_, span := module.StartRequest(context.Background(), route.TracedRequest{Header: parentHeader(), Method: http.MethodGet, ContinueParent: true})
		span.End(http.StatusOK)
		ended := recorder.Ended()
		require.Len(t, ended, 1, "a sampled trusted parent is recorded even at ratio 0")
		require.Equal(t, parentTraceID, ended[0].SpanContext().TraceID().String())
		require.Equal(t, "00f067aa0ba902b7", ended[0].Parent().SpanID().String())
	})
	t.Run("untrusted client", func(t *testing.T) {
		t.Parallel()
		module, recorder := recordingModule(t, 1)
		_, span := module.StartRequest(context.Background(), route.TracedRequest{Header: parentHeader(), Method: http.MethodGet})
		span.End(http.StatusOK)
		ended := recorder.Ended()
		require.Len(t, ended, 1)
		require.NotEqual(t, parentTraceID, ended[0].SpanContext().TraceID().String())
		require.False(t, ended[0].Parent().IsValid(), "untrusted request continued a caller-chosen trace")
	})
	t.Run("untrusted client cannot force sampling", func(t *testing.T) {
		t.Parallel()
		module, recorder := recordingModule(t, 0)
		_, span := module.StartRequest(context.Background(), route.TracedRequest{Header: parentHeader(), Method: http.MethodGet})
		span.End(http.StatusOK)
		require.Empty(t, recorder.Ended())
	})
}

func TestStoreOperationSpansOnlyJoinARecordingTrace(t *testing.T) {
	t.Parallel()

	module, recorder := recordingModule(t, 1)
	users := &correlatingUserStore{}
	layer, err := timerlayer.New(userRoot{users: users}, timerlayer.NopRecorder{}, timerlayer.Options{Tracer: module})
	require.NoError(t, err)
	_, err = layer.User().GetByEmail(context.Background(), "student@example.edu")
	require.Error(t, err)
	require.Empty(t, recorder.Ended(), "a store operation without a parent started a root trace")

	ctx, request := module.StartRequest(context.Background(), route.TracedRequest{Header: http.Header{}, Method: http.MethodPost})
	_, err = layer.User().GetByEmail(ctx, "student@example.edu")
	require.Error(t, err)
	traceID, spanID, ok := Correlation(users.ctx)
	require.True(t, ok)
	request.End(http.StatusOK)

	ended := recorder.Ended()
	require.Len(t, ended, 2)
	store := ended[0]
	require.Equal(t, "store user.get_by_email", store.Name())
	require.Equal(t, traceID, store.SpanContext().TraceID().String())
	require.Equal(t, spanID, store.SpanContext().SpanID().String())
	require.Equal(t, ended[1].SpanContext().SpanID(), store.Parent().SpanID())
	require.Equal(t, map[attribute.Key]attribute.Value{
		"db.system.name":    attribute.StringValue("postgresql"),
		"db.operation.name": attribute.StringValue("user.get_by_email"),
	}, attributes(store))
	require.Equal(t, sdktrace.Status{Code: codes.Error, Description: "store operation failed"}, store.Status())
}

func TestJobExecutionSpanIsANewRootWithItsOutcome(t *testing.T) {
	t.Parallel()

	module, recorder := recordingModule(t, 1)
	parentCtx, request := module.StartRequest(context.Background(), route.TracedRequest{Header: http.Header{}, Method: http.MethodGet})
	_, finish := module.StartExecution(parentCtx, "mail_delivery")
	finish(job.ExecutionRetryableFailure)
	request.End(http.StatusOK)

	ended := recorder.Ended()
	require.Len(t, ended, 2)
	job := ended[0]
	require.Equal(t, "job mail_delivery", job.Name())
	require.False(t, job.Parent().IsValid(), "Job execution joined the caller's trace")
	require.Equal(t, map[attribute.Key]attribute.Value{
		"proctor.job.type":    attribute.StringValue("mail_delivery"),
		"proctor.job.outcome": attribute.StringValue("retryable_failure"),
	}, attributes(job))
	require.Equal(t, codes.Error, job.Status().Code)
}

func TestDisabledModuleStartsNothing(t *testing.T) {
	t.Parallel()

	module, err := New(config.Default().Tracing, Identity{}, nil)
	require.NoError(t, err)
	require.False(t, module.Enabled())
	ctx, span := module.StartRequest(context.Background(), route.TracedRequest{Header: parentHeader(), Method: http.MethodGet, ContinueParent: true})
	span.SetRoute("/health/live")
	span.End(http.StatusOK)
	_, _, ok := Correlation(ctx)
	require.False(t, ok, "disabled tracing produced a correlatable span")
	require.NoError(t, module.Close(context.Background()))
	require.NoError(t, module.Close(context.Background()))
}

func TestEnabledModuleConstructsAnExporterWithoutConnecting(t *testing.T) {
	t.Parallel()

	settings := config.Default().Tracing
	settings.Enabled = true
	settings.EndpointURL = "http://127.0.0.1:1/v1/traces"
	module, err := New(settings, Identity{Version: "test", NodeID: "node-a"}, nil)
	require.NoError(t, err)
	require.True(t, module.Enabled())
	require.NoError(t, module.Close(context.Background()), "closing with no buffered spans needs no collector")
	require.NoError(t, module.Close(context.Background()))
}

func TestExporterSendsOnlyConfiguredHeaders(t *testing.T) {
	// The SDK would otherwise read this standard variable; configuration must
	// remain the only header source.
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "x-ambient=ambient-value")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS", "x-ambient=ambient-value")

	received := make(chan http.Header, 4)
	collector := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		received <- request.Header.Clone()
		writer.Header().Set("Content-Type", "application/x-protobuf")
	}))
	t.Cleanup(collector.Close)

	settings := config.Default().Tracing
	settings.Enabled = true
	settings.EndpointURL = collector.URL + "/v1/traces"
	settings.Headers = map[string]string{"x-honeycomb-team": "team-key"}
	module, err := New(settings, Identity{Version: "test", NodeID: "node-a"}, nil)
	require.NoError(t, err)
	_, end := module.StartExecution(context.Background(), "test.job")
	end(job.ExecutionSucceeded)
	require.NoError(t, module.Close(context.Background()))

	select {
	case header := <-received:
		require.Equal(t, "team-key", header.Get("X-Honeycomb-Team"))
		require.Empty(t, header.Get("X-Ambient"), "the exporter read OTEL_EXPORTER_OTLP_HEADERS")
	default:
		require.FailNow(t, "no export reached the collector")
	}
}

// userRoot is a store whose only aggregate is users, enough for the timing
// layer to time one operation.
type userRoot struct {
	store.Store
	users store.UserStore
}

func (s userRoot) User() store.UserStore { return s.users }

// correlatingUserStore fails every lookup and keeps the context the timing
// layer handed it, which carries the store span.
type correlatingUserStore struct {
	store.UserStore
	ctx context.Context
}

func (s *correlatingUserStore) GetByEmail(ctx context.Context, _ string) (*model.User, error) {
	s.ctx = ctx
	return nil, errors.New("lookup failed")
}
