// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"testing/fstest"

	"github.com/sudosylabs/proctor/packages/vfs"
	"github.com/sudosylabs/proctor/server/app"
	"github.com/sudosylabs/proctor/server/app/exam/assistant"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/httpapi"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/platform"
	"github.com/sudosylabs/proctor/server/platform/executionhost"
	"github.com/sudosylabs/proctor/server/plugin"
	"github.com/sudosylabs/proctor/server/store"
	"github.com/sudosylabs/proctor/server/store/localcachelayer"
	"github.com/sudosylabs/proctor/server/store/retrylayer"
	"github.com/sudosylabs/proctor/server/store/timerlayer"
	"github.com/sudosylabs/proctor/server/webui"
)

// TestingOverrides replaces individual runtime capabilities for tests. A nil
// field keeps the production choice. A provided capability must be fully
// built; the node takes ownership and closes it on construction failure and
// on Close, as in production.
type TestingOverrides struct {
	// ServiceEnvironment defaults to test, independent of process environment
	// and build tags. Only explicit dev tests bypass Desktop compatibility.
	ServiceEnvironment ServiceEnvironment
	Configuration      *config.Store
	Logger             *logging.Logger
	Persistence        store.Store
	StoreRetry         *retrylayer.Policy
	StoreMetrics       timerlayer.Recorder
	StoreLocalCache    localcachelayer.Cache
	StoreCachePolicy   *localcachelayer.Policy
	StoreCacheMetrics  localcachelayer.Recorder
	// A nil MailMetrics keeps the production recorder.
	MailMetrics mail.DeliveryRecorder
	Cache       platform.Cache
	Cluster     platform.Cluster
	Mailer      platform.Mailer
	Filesystem  vfs.FileSystem
	// DesktopBuildCatalog replaces the immutable compiled catalog only for the
	// test graph. Production never accepts catalog entries from configuration.
	DesktopBuildCatalog []model.DesktopBuildTuple
	// Plugins are activated as if compiled in: each one replaces the
	// built-in plugin with the same ID, and the others follow the built-in
	// ones.
	Plugins []plugin.Plugin
	// ExecutionHostDial replaces how the node reaches its configured
	// execution hosts, so tests can use in-process hosts. It also accepts
	// hosts without isolation. Production always dials the configured
	// addresses and requires isolation.
	ExecutionHostDial executionhost.Dialer
	// AssistantGateway replaces the configured model providers, so tests
	// script the model. It applies only when the configuration enables the
	// assistant; production always reaches the configured providers.
	AssistantGateway assistant.ModelGateway
	// AssistantLimits lets tests exercise expiry without waiting five minutes.
	AssistantLimits assistant.Limits
	// AllowMissingJobs is for lifecycle-only tests. Production always
	// requires durable job persistence and a job runtime.
	AllowMissingJobs bool
	// BuildInfo replaces the served build information when any field is set.
	BuildInfo httpapi.BuildInfo
	// WebappFiles replaces the immutable packaged browser distribution. A nil
	// value uses a minimal build-matched distribution owned by NewForTesting.
	WebappFiles fs.FS
	// BootstrapSecretWriter captures the explicit loopback-development secret.
	// Production writes it directly to the controlling terminal.
	BootstrapSecretWriter io.Writer
}

// TestingRuntime exposes the built node's handles. Only Server owns
// resources, and the caller must close it.
type TestingRuntime struct {
	Server      *Server
	Application *app.App
	Handler     http.Handler
}

// NewForTesting builds the production graph with overrides, through the same
// path as New, so startup, readiness and shutdown behave as in production.
func NewForTesting(ctx context.Context, overrides TestingOverrides) (*TestingRuntime, error) {
	result, err := composeNode(ctx, testingCompositionInput(overrides))
	if err != nil {
		return nil, err
	}
	return &TestingRuntime{
		Server:      result.server,
		Application: result.application,
		Handler:     result.handler,
	}, nil
}

// testingCompositionInput completes overrides with the test defaults: the
// test service environment, a discarded bootstrap secret, and a minimal
// build-matched webapp.
func testingCompositionInput(overrides TestingOverrides) compositionInput {
	if overrides.ServiceEnvironment == "" {
		overrides.ServiceEnvironment = ServiceEnvironmentTest
	}
	if overrides.BootstrapSecretWriter == nil {
		overrides.BootstrapSecretWriter = io.Discard
	}
	if overrides.WebappFiles == nil {
		overrides.WebappFiles = testingWebappFiles(overrides.BuildInfo)
	}
	return compositionInput{
		serviceEnvironment: overrides.ServiceEnvironment,
		overrides:          overrides,
	}
}

func testingWebappFiles(build httpapi.BuildInfo) fs.FS {
	if build == (httpapi.BuildInfo{}) {
		current := app.CurrentBuildInfo()
		build.Version, build.Commit = current.Version, current.Commit
	}
	manifest, _ := json.Marshal(map[string]any{
		"version": build.Version,
		"commit":  build.Commit,
	})
	return fstest.MapFS{
		webui.BuildManifestName: {Data: manifest},
		"index.html":            {Data: []byte("<!doctype html><title>Proctor test webapp</title>")},
	}
}
