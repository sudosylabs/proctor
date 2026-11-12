// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"

	"time"

	"github.com/sudosylabs/proctor/server/app"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/filecontent"
	"github.com/sudosylabs/proctor/server/httpapi"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/localization"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/platform"
	"github.com/sudosylabs/proctor/server/pluginhost"
	"github.com/sudosylabs/proctor/server/tracing"
	"github.com/sudosylabs/proctor/server/websocket"
	"github.com/sudosylabs/proctor/server/webui"
)

type compositionInput struct {
	serviceEnvironment ServiceEnvironment
	configPath         string
	// schema is schemaRequireCurrent for offline host commands, which replace
	// startup migration with a strict schema version check.
	schema    schemaPolicy
	overrides TestingOverrides
	// observeRelease receives the release record of a failed consumer
	// composition before its owners are closed. The real hub and transport
	// close without an observable effect, so
	// TestConsumerConstructionFailuresUnwindExactlyOnce sets it to prove which
	// consumers each failing phase had registered. Production leaves it nil.
	observeRelease func(compositionRelease)
}

var errDurableJobRuntimeUnavailable = errors.New("application Job runtime is unavailable")

// compositionResult is the assembled node. server owns every lifecycle;
// application and handler are borrowed behavioral handles that only
// NewForTesting hands out, and production construction discards them.
type compositionResult struct {
	server      *Server
	application *app.App
	handler     http.Handler
}

// composeNode is the sole construction recipe for production and tests. Each
// phase completes before the next becomes observable; construction remains
// inert and transfers all lifecycle authority into the returned Server.
func composeNode(ctx context.Context, input compositionInput) (*compositionResult, error) {
	if !input.serviceEnvironment.valid() {
		return nil, errors.New("invalid service environment")
	}

	// 1. Acquire and decorate infrastructure under one temporary owner.
	infrastructure, err := openRuntimeInfrastructure(ctx, input.configPath, input.overrides, input.schema)
	if err != nil {
		return nil, fmt.Errorf("acquire infrastructure: %w", err)
	}

	// 2. Atomically transfer ownership to Platform and capture one immutable
	// construction snapshot plus a lifecycle-free capability projection.
	applicationPlatform, snapshot, capabilities, err := infrastructure.acceptPlatform(ctx)
	if err != nil {
		return nil, fmt.Errorf("accept platform ownership: %w", err)
	}

	logStartupInfrastructure(snapshot, capabilities.logger, capabilities.migration)
	capabilities.logger.Info("service environment selected",
		logging.String("environment", string(input.serviceEnvironment)),
		logging.Bool("desktop_compatibility_enforced", input.serviceEnvironment != ServiceEnvironmentDev),
	)

	// 3. Construct consumers in dependency order. A failure releases only the
	// consumers already built, followed by Platform.
	release := compositionRelease{
		platform:        applicationPlatform,
		shutdownTimeout: snapshot.Server.ShutdownTimeout.Duration,
		metrics:         capabilities.metrics,
		tracing:         capabilities.tracing,
		plugins:         capabilities.plugins,
	}
	result, err := composeConsumers(ctx, applicationPlatform, snapshot, capabilities, input, &release)
	if err != nil {
		if input.observeRelease != nil {
			input.observeRelease(release)
		}
		return nil, release.fail(err)
	}
	return result, nil
}

// composeConsumers constructs every consumer of the accepted Platform and
// assembles the inert Server. It records each closable consumer in release as
// soon as it is built; the caller releases them when composeConsumers fails.
func composeConsumers(
	ctx context.Context,
	applicationPlatform *platform.Service,
	snapshot config.Config,
	capabilities constructionCapabilities,
	input compositionInput,
	release *compositionRelease,
) (*compositionResult, error) {
	localizer, mailRenderer, err := composeLocalization(snapshot)
	if err != nil {
		return nil, err
	}

	applicationDeps, err := composeApplicationDependencies(ctx, snapshot, capabilities, input, mailRenderer)
	if err != nil {
		return nil, err
	}

	application, err := app.New(applicationDeps)
	if err != nil {
		return nil, fmt.Errorf("construct application: %w", err)
	}

	clusterFanout, err := newRealtimeClusterAdapter(capabilities.cluster)
	if err != nil {
		return nil, fmt.Errorf("construct realtime cluster adapter: %w", err)
	}
	if err = application.Realtime.AttachClusterFanout(clusterFanout); err != nil {
		return nil, fmt.Errorf("attach realtime cluster fan-out: %w", err)
	}

	readiness, err := composeReadiness(applicationPlatform)
	if err != nil {
		return nil, err
	}

	buildInfo := resolveBuildInfo(input.overrides.BuildInfo)
	webSocketHub, err := websocket.NewHub(
		application.RealtimeAdmission,
		application.Attempts,
		capabilities.logger,
		snapshot.Server.PublicURL,
		capabilities.nodeID,
		localizer,
	)
	if err != nil {
		return nil, fmt.Errorf("construct WebSocket hub: %w", err)
	}

	release.websocket = webSocketHub.Close
	if capabilities.metrics.Enabled() {
		if err = webSocketHub.AttachRecorder(capabilities.metrics); err != nil {
			return nil, fmt.Errorf("attach WebSocket metrics: %w", err)
		}
	}

	if err = application.Realtime.AttachSink(webSocketHub); err != nil {
		return nil, fmt.Errorf("attach realtime sink: %w", err)
	}

	httpSettings, err := httpAPISettings(readiness, buildInfo, snapshot, capabilities, webSocketHub, localizer)
	if err != nil {
		return nil, err
	}

	httpAPI, err := httpapi.New(application, httpSettings)
	if err != nil {
		return nil, fmt.Errorf("construct HTTP API: %w", err)
	}

	release.transport = httpAPI.Close
	rootTransport, err := composeRootTransport(httpAPI, buildInfo, snapshot, input.overrides.WebappFiles)
	if err != nil {
		return nil, err
	}

	release.transport = rootTransport.Close
	// An absent engine stays an untyped nil so the Server skips the Job stage.
	var jobRuntime runtimeComponent
	if engine := application.JobEngine(); engine != nil {
		jobRuntime = engine
	}
	if jobRuntime == nil && !input.overrides.AllowMissingJobs {
		return nil, fmt.Errorf("require durable Job runtime: %w", errDurableJobRuntimeUnavailable)
	}

	servingLease, err := newServingNodeLeaseRuntime(capabilities.persistence.ServingNodeLease(), capabilities.nodeID)
	if err != nil {
		return nil, fmt.Errorf("construct serving node lease: %w", err)
	}

	// 4. Assemble one inert lifecycle owner. No listener, goroutine, readiness,
	// or transport lifecycle operation runs during composition.
	node := &Server{components: runtimeComponents{
		platform:              applicationPlatform,
		reconciler:            application.Identity,
		administratorRecovery: application.Identity,
		settings:              runtimeSettingsFromConfig(snapshot.Server),
		logger:                capabilities.logger,
		jobs:                  jobRuntime,
		servingLease:          servingLease,
		transport:             rootTransport,
		websocket:             webSocketHub,
		plugins:               capabilities.plugins,
		metrics:               capabilities.metrics,
		tracing:               capabilities.tracing,
		readiness:             readiness,
		listen:                net.Listen,
		newHTTP:               newHTTPServer,
	}}
	return &compositionResult{server: node, application: application, handler: rootTransport}, nil
}

// composeLocalization opens the embedded catalogs and templates and constructs
// the localizer and the mail renderer that translates through it.
func composeLocalization(snapshot config.Config) (*localization.Localizer, mail.Renderer, error) {
	catalogFiles, err := runtimeAssetDirectory("i18n")
	if err != nil {
		return nil, nil, fmt.Errorf("open localization catalogs: %w", err)
	}

	localizer, err := localization.New(catalogFiles, snapshot.Localization.DefaultLocale)
	if err != nil {
		return nil, nil, fmt.Errorf("construct localizer: %w", err)
	}

	templateFiles, err := runtimeAssetDirectory("templates")
	if err != nil {
		return nil, nil, fmt.Errorf("open mail templates: %w", err)
	}

	mailRenderer, err := mail.NewRendererWithPolicy(templateFiles, localizer, mail.RendererPolicy{
		AllowLoopbackHTTPDevelopment: explicitLoopbackHTTPDevelopment(snapshot.Server.PublicURL),
		SupportAddress:               snapshot.Mail.SupportAddress,
		DisplayTimezone:              snapshot.Mail.DisplayTimezone,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("construct mail renderer: %w", err)
	}

	return localizer, mailRenderer, nil
}

// composeApplicationDependencies constructs File Content and projects the application dependencies with telemetry and bootstrap protection.
func composeApplicationDependencies(
	ctx context.Context,
	snapshot config.Config,
	capabilities constructionCapabilities,
	input compositionInput,
	mailRenderer mail.Renderer,
) (app.Dependencies, error) {
	var contentRecorder filecontent.WorkRecorder
	if capabilities.metrics.Enabled() {
		contentRecorder = capabilities.metrics.WorkRecorder("file_content")
	}
	examExportFormat, offered := capabilities.plugins.ExportFormat(filecontent.ZipExportMediaType)
	if !offered {
		capabilities.logger.Warn("Exam exports are unavailable: no active plugin offers the zip export format")
	}
	content, err := filecontent.New(
		capabilities.filesystem,
		filecontent.Policy{
			MaximumConcurrentOperations: snapshot.FileContent.MaximumConcurrentOperations,
			ExamExportFormat:            examExportFormat,
		},
		contentRecorder,
	)
	if err != nil {
		return app.Dependencies{}, fmt.Errorf("construct file content: %w", err)
	}
	applicationDeps, err := applicationDependencies(capabilities, snapshot, content, mailRenderer)
	if err != nil {
		return app.Dependencies{}, fmt.Errorf("project application dependencies: %w", err)
	}
	applicationDeps.SkipDesktopCompatibility = input.serviceEnvironment == ServiceEnvironmentDev
	if input.overrides.DesktopBuildCatalog != nil {
		applicationDeps.DesktopBuildCatalog = append([]model.DesktopBuildTuple(nil), input.overrides.DesktopBuildCatalog...)
	}
	attachApplicationTelemetry(&applicationDeps, capabilities, input.overrides.MailMetrics)
	var installations bootstrapStatusStore
	if current := capabilities.persistence.Installation(); current != nil {
		installations = current
	}
	bootstrapOutput := input.overrides.BootstrapSecretWriter
	if bootstrapOutput == nil {
		bootstrapOutput = os.Stderr
	}
	applicationDeps.BootstrapProtection, err = resolveBootstrapProtection(ctx, snapshot, installations, bootstrapOutput)
	if err != nil {
		return app.Dependencies{}, fmt.Errorf("protect installation bootstrap: %w", err)
	}
	return applicationDeps, nil
}

// attachApplicationTelemetry wires Job, operational, password-work and mail delivery recorders and the Job tracer into the dependencies.
func attachApplicationTelemetry(
	applicationDeps *app.Dependencies,
	capabilities constructionCapabilities,
	mailMetrics mail.DeliveryRecorder,
) {
	if capabilities.metrics.Enabled() {
		applicationDeps.JobRecorder = jobMetricsRecorder{metrics: capabilities.metrics}
		applicationDeps.OperationalRecorder = applicationMetricsRecorder{metrics: capabilities.metrics}
		applicationDeps.PasswordWorkRecorder = capabilities.metrics.WorkRecorder("password")
	}
	if capabilities.tracing.Enabled() {
		applicationDeps.JobTracer = jobTracing{tracing: capabilities.tracing}
	}
	// Mail observations go to the test recorder, then Prometheus, then the
	// operational collector. With neither of the first two, the dependencies
	// keep the operational collector applicationDependencies built.
	var mailRecorders []mail.DeliveryRecorder
	if mailMetrics != nil {
		mailRecorders = append(mailRecorders, mailMetrics)
	}
	if capabilities.metrics.Enabled() {
		mailRecorders = append(mailRecorders, newPrometheusMailRecorder(capabilities.metrics))
	}
	if len(mailRecorders) > 0 {
		mailDeliveryRecorder, mailMetricsReader := newMailTelemetry(capabilities.logger, mailRecorders...)
		applicationDeps.MailDeliveryRecorder = mailDeliveryRecorder
		applicationDeps.MailMetricsReader = mailMetricsReader
	}
}

// composeReadiness builds readiness over a live, bounded probe of the mandatory platform dependencies.
func composeReadiness(applicationPlatform *platform.Service) (*app.Health, error) {
	// Readiness requires both the lifecycle flag and a live, bounded probe of
	// the mandatory platform dependencies (PostgreSQL, cache, cluster, VFS).
	readiness, err := app.NewHealth(app.HealthDependencies{
		Probe:    applicationPlatform.CheckDependencies,
		Timeout:  app.DefaultReadinessProbeTimeout,
		Interval: time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("construct readiness: %w", err)
	}

	return readiness, nil
}

// resolveBuildInfo returns the overridden build information or, when none is set, this binary's own.
func resolveBuildInfo(buildInfo httpapi.BuildInfo) httpapi.BuildInfo {
	if buildInfo == (httpapi.BuildInfo{}) {
		current := app.CurrentBuildInfo()
		buildInfo = httpapi.BuildInfo{
			Version:   current.Version,
			Commit:    current.Commit,
			BuildTime: current.BuildTime,
			GoVersion: current.GoVersion,
		}
	}

	return buildInfo
}

// httpAPISettings projects readiness, the WebSocket hub, the localizer and server settings into the HTTP API settings.
func httpAPISettings(
	readiness *app.Health,
	buildInfo httpapi.BuildInfo,
	snapshot config.Config,
	capabilities constructionCapabilities,
	webSocketHub *websocket.Hub,
	localizer *localization.Localizer,
) (httpapi.Settings, error) {
	var httpMetrics route.Metrics
	if capabilities.metrics.Enabled() {
		httpMetrics = capabilities.metrics
	}

	var httpTracer route.Tracing
	if capabilities.tracing.Enabled() {
		httpTracer = httpTracing{tracing: capabilities.tracing}
	}

	trustedProxies, err := config.ParseTrustedProxies(snapshot.Server.TrustedProxies)
	if err != nil {
		return httpapi.Settings{}, fmt.Errorf("parse trusted proxies: %w", err)
	}

	return httpapi.Settings{
		Logger:                  capabilities.logger,
		Localizer:               localizer,
		Health:                  readiness,
		Metrics:                 httpMetrics,
		Tracing:                 httpTracer,
		BuildInfo:               buildInfo,
		ActivePlugins:           activePluginSummaries(capabilities.plugins),
		PublicURL:               snapshot.Server.PublicURL,
		MaxBodyBytes:            snapshot.Server.MaxBodyBytes,
		ReadTimeout:             snapshot.Server.ReadTimeout.Duration,
		WriteTimeout:            snapshot.Server.WriteTimeout.Duration,
		TrustedProxies:          trustedProxies,
		RecentAuthenticationTTL: snapshot.Authentication.RecentAuthenticationTTL.Duration,
		NodeID:                  capabilities.nodeID,
		WebSocket:               webSocketHub,
	}, nil
}

// composeRootTransport constructs the hosted webapp and wraps it with the HTTP API in the root transport.
func composeRootTransport(
	httpAPI *route.API,
	buildInfo httpapi.BuildInfo,
	snapshot config.Config,
	overrideWebappFiles fs.FS,
) (runtimeTransport, error) {
	webappFiles := overrideWebappFiles
	if webappFiles == nil {
		webappFiles = os.DirFS(snapshot.Server.WebappDirectory)
	}
	webappHandler, err := webui.New(webappFiles, webui.Options{
		BuildVersion:     buildInfo.Version,
		BuildCommit:      buildInfo.Commit,
		NotFound:         httpAPI.NotFoundHandler(),
		MethodNotAllowed: httpAPI.MethodNotAllowedHandler(http.MethodGet, http.MethodHead),
	})
	if err != nil {
		return nil, fmt.Errorf("construct hosted webapp: %w", err)
	}
	rootTransport, err := newRootHTTPTransport(httpAPI, httpAPI, webappHandler, snapshot.Server.StrictTransportSecurity())
	if err != nil {
		return nil, fmt.Errorf("compose HTTP transports: %w", err)
	}
	return rootTransport, nil
}

// compositionRelease records the owners a failed composition must release:
// the HTTP transport and WebSocket hub once built, then metrics and Platform.
// A later transport, such as the root HTTP transport that owns the API
// transport, replaces the one it wraps.
type compositionRelease struct {
	platform        runtimeComponent
	shutdownTimeout time.Duration
	metrics         runtimeMetrics
	tracing         *tracing.Module
	transport       func() error
	websocket       func(context.Context) error
	plugins         *pluginhost.Host
}

// fail joins cause with the release of every owner built so far, closing the
// transport, the WebSocket hub, metrics, tracing, and Platform in that order
// under one fresh shutdown budget; a failed composition has spent none of it.
func (r compositionRelease) fail(cause error) error {
	budget, cancel := context.WithTimeout(context.Background(), r.shutdownTimeout)
	defer cancel()
	var transportErr, websocketErr, metricsErr, tracingErr error
	if r.transport != nil {
		transportErr = r.transport()
	}
	if r.websocket != nil {
		websocketErr = r.websocket(budget)
	}
	pluginsErr := r.plugins.Close(budget)
	if r.metrics != nil {
		metricsErr = r.metrics.Close(budget)
	}
	if r.tracing != nil {
		tracingErr = r.tracing.Close(budget)
	}
	return errors.Join(cause, transportErr, websocketErr, pluginsErr, errors.Join(metricsErr, tracingErr, r.platform.Close(budget)))
}
