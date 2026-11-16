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
	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/cluster"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/filecontent"
	"github.com/sudosylabs/proctor/server/httpapi"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/localization"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/metrics"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/platform"
	"github.com/sudosylabs/proctor/server/platform/executionhost"
	"github.com/sudosylabs/proctor/server/plugin/zipexport"
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
	// observeRelease sees a failed composition's release record before its
	// owners close. Closing the real hub and transport has no observable
	// effect, so TestConsumerConstructionFailuresUnwindExactlyOnce uses it.
	// Production leaves it nil.
	observeRelease func(compositionRelease)
}

var errDurableJobRuntimeUnavailable = errors.New("application Job runtime is unavailable")

// compositionResult is the assembled node. server owns every lifecycle;
// application and handler are borrowed handles that only NewForTesting hands
// out.
type compositionResult struct {
	server      *Server
	application *app.App
	handler     http.Handler
}

// composeNode is the one construction path for production and tests. It
// stays inert and hands every lifecycle to the returned Server.
func composeNode(ctx context.Context, input compositionInput) (*compositionResult, error) {
	if !input.serviceEnvironment.valid() {
		return nil, errors.New("invalid service environment")
	}

	infrastructure, err := openRuntimeInfrastructure(ctx, input.configPath, input.overrides, input.schema)
	if err != nil {
		return nil, fmt.Errorf("acquire infrastructure: %w", err)
	}

	// Ownership moves to the platform in one step; capabilities carry no
	// lifecycle of their own.
	applicationPlatform, snapshot, capabilities, err := infrastructure.acceptPlatform(ctx)
	if err != nil {
		return nil, fmt.Errorf("accept platform ownership: %w", err)
	}

	logStartupInfrastructure(snapshot, capabilities.logger, capabilities.migration)
	capabilities.logger.Info("service environment selected",
		logging.String("environment", string(input.serviceEnvironment)),
		logging.Bool("desktop_compatibility_enforced", input.serviceEnvironment != ServiceEnvironmentDev),
	)

	// From here a failure releases only the consumers already built, then the
	// platform.
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

// composeConsumers constructs every consumer of the accepted platform and
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

	// A lifecycle-only graph without persistence has no saved settings to
	// follow.
	var reconciler *pluginhost.Reconciler
	if saved := capabilities.persistence.PluginSettings(); saved != nil {
		reconciler = pluginhost.NewReconciler(
			saved, capabilities.plugins, snapshot, capabilities.logger, capabilities.pluginSettingsCount,
		)
	}
	applicationDeps, err := composeApplicationDependencies(ctx, snapshot, capabilities, input, mailRenderer)
	if err != nil {
		return nil, err
	}
	applicationDeps.Plugins = pluginhost.Catalog(capabilities.plugins, reconciler)
	environments, err := composeEnvironments(snapshot, capabilities, input)
	if err != nil {
		return nil, err
	}
	if environments != nil {
		applicationDeps.EnvironmentWaker = environments.Waker()
		applicationDeps.EnvironmentSettles = environments.Settles()
		applicationDeps.EnvironmentDiagnostics = environments.Report
		applicationDeps.EnvironmentHosts = environments.Hosts()
	}

	application, err := app.New(applicationDeps)
	if err != nil {
		return nil, fmt.Errorf("construct application: %w", err)
	}
	// An absent runtime stays an untyped nil so the Server skips its stage.
	var environmentRuntime runtimeComponent
	if environments != nil {
		if err = environments.Complete(application.Attempts); err != nil {
			return nil, err
		}
		environmentRuntime = environments
	}
	if err = application.Realtime.AttachClusterFanout(capabilities.cluster); err != nil {
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

	servingLease, err := cluster.NewServingLease(capabilities.persistence.ServingNodeLease(), capabilities.nodeID)
	if err != nil {
		return nil, fmt.Errorf("construct serving node lease: %w", err)
	}

	// No listener, goroutine, readiness or transport lifecycle operation runs
	// during composition.
	node := &Server{components: runtimeComponents{
		platform:              applicationPlatform,
		reconciler:            application.Identity,
		administratorRecovery: application.Identity,
		settings:              runtimeSettingsFromConfig(snapshot.Server),
		logger:                capabilities.logger,
		jobs:                  jobRuntime,
		environments:          environmentRuntime,
		servingLease:          servingLease,
		transport:             rootTransport,
		websocket:             webSocketHub,
		plugins:               capabilities.plugins,
		pluginReconciler:      reconciler,
		metrics:               capabilities.metrics,
		tracing:               capabilities.tracing,
		readiness:             readiness,
		listen:                net.Listen,
		newHTTP:               newHTTPServer,
	}}
	return &compositionResult{server: node, application: application, handler: rootTransport}, nil
}

// composeEnvironments builds this node's execution environments, or nil when
// the installation runs none. Development accepts hosts that share the
// machine's kernel; a test's dialer replaces dialing a host.
func composeEnvironments(
	snapshot config.Config,
	capabilities constructionCapabilities,
	input compositionInput,
) (*environment.Node, error) {
	persistence := capabilities.persistence
	if !snapshot.Execution.Enabled || persistence.AttemptEnvironment() == nil {
		return nil, nil
	}
	dial := input.overrides.ExecutionHostDial
	hosts, err := executionhost.New(executionhost.Config{Hosts: snapshot.Execution.Hosts, Dial: dial,
		AllowUnisolated: input.serviceEnvironment == ServiceEnvironmentDev || dial != nil})
	if err != nil {
		return nil, fmt.Errorf("construct execution hosts: %w", err)
	}
	node, err := environment.NewNode(environment.NodeDependencies{
		Cluster:      capabilities.cluster,
		Environments: persistence.AttemptEnvironment(),
		Evidence:     persistence.ExamAttemptWorkspace(),
		Leases:       persistence.AttemptRuntimeLease(),
		Hosts:        hosts,
		NodeID:       capabilities.nodeID,
		Owners:       snapshot.Execution.Owners,
		Logger:       capabilities.logger,
	})
	if err != nil {
		return nil, errors.Join(err, hosts.Close())
	}
	return node, nil
}

func composeLocalization(snapshot config.Config) (*localization.Localizer, *mail.TemplateRenderer, error) {
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

func composeApplicationDependencies(
	ctx context.Context,
	snapshot config.Config,
	capabilities constructionCapabilities,
	input compositionInput,
	mailRenderer *mail.TemplateRenderer,
) (app.Dependencies, error) {
	var contentRecorder filecontent.WorkRecorder
	if capabilities.metrics.Enabled() {
		contentRecorder = capabilities.metrics.WorkRecorder("file_content")
	}
	if _, offered := capabilities.plugins.ExportFormat(zipexport.MediaType); !offered {
		capabilities.logger.Warn("Exam exports are unavailable: no active plugin offers the zip export format")
	}
	content, err := filecontent.New(
		capabilities.filesystem,
		filecontent.Policy{
			MaximumConcurrentOperations: snapshot.FileContent.MaximumConcurrentOperations,
			ExamExportFormat:            pluginhost.LiveExportFormat(capabilities.plugins, zipexport.MediaType),
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
	bootstrapOutput := input.overrides.BootstrapSecretWriter
	if bootstrapOutput == nil {
		bootstrapOutput = os.Stderr
	}
	applicationDeps.BootstrapProtection, err = identity.ResolveBootstrapProtection(ctx,
		snapshot.Authentication.Bootstrap.Secret, snapshot.Authentication.Bootstrap.DevelopmentMode,
		capabilities.persistence.Installation(), bootstrapOutput)
	if err != nil {
		return app.Dependencies{}, fmt.Errorf("protect installation bootstrap: %w", err)
	}
	return applicationDeps, nil
}

func attachApplicationTelemetry(
	applicationDeps *app.Dependencies,
	capabilities constructionCapabilities,
	mailMetrics mail.DeliveryRecorder,
) {
	if capabilities.metrics.Enabled() {
		applicationDeps.JobRecorder = capabilities.metrics.JobRecorder()
		applicationDeps.OperationalRecorder = capabilities.metrics
		applicationDeps.PasswordWorkRecorder = capabilities.metrics.WorkRecorder("password")
	}
	if capabilities.tracing.Enabled() {
		applicationDeps.JobTracer = capabilities.tracing
	}
	// Mail observations go to the test recorder, then Prometheus, then the
	// operational collector. With neither of the first two, the dependencies
	// keep the operational collector applicationDependencies built.
	var mailRecorders []mail.DeliveryRecorder
	if mailMetrics != nil {
		mailRecorders = append(mailRecorders, mailMetrics)
	}
	if capabilities.metrics.Enabled() {
		mailRecorders = append(mailRecorders, capabilities.metrics.MailRecorder())
	}
	if len(mailRecorders) > 0 {
		mailDeliveryRecorder, mailMetricsReader := metrics.NewMailTelemetry(capabilities.logger, mailRecorders...)
		applicationDeps.MailDeliveryRecorder = mailDeliveryRecorder
		applicationDeps.MailMetricsReader = mailMetricsReader
	}
}

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
		httpTracer = capabilities.tracing
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
		Plugins:                 pluginDirectory{host: capabilities.plugins},
		PublicURL:               snapshot.Server.PublicURL,
		DocumentationURL:        snapshot.Server.DocumentationURL,
		MaxBodyBytes:            snapshot.Server.MaxBodyBytes,
		ReadTimeout:             snapshot.Server.ReadTimeout.Duration,
		WriteTimeout:            snapshot.Server.WriteTimeout.Duration,
		TrustedProxies:          trustedProxies,
		RecentAuthenticationTTL: snapshot.Authentication.RecentAuthenticationTTL.Duration,
		NodeID:                  capabilities.nodeID,
		WebSocket:               webSocketHub,
		BinarySockets:           webSocketHub,
	}, nil
}

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

// compositionRelease records the owners a failed composition must close. A
// later transport, such as the root transport that owns the API, replaces the
// one it wraps.
type compositionRelease struct {
	platform        runtimeComponent
	shutdownTimeout time.Duration
	metrics         runtimeMetrics
	tracing         *tracing.Module
	transport       func() error
	websocket       func(context.Context) error
	plugins         *pluginhost.Host
}

// fail joins cause with closing every owner built so far, under a fresh
// shutdown budget; a failed composition has spent none of it.
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
