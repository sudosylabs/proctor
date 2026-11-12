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
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/packages/vfs"
	"github.com/sudosylabs/proctor/server/cluster"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/httpapi"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/platform"
	"github.com/sudosylabs/proctor/server/plugin"
	"github.com/sudosylabs/proctor/server/store"
	"github.com/sudosylabs/proctor/server/store/sqlstore"
	"github.com/sudosylabs/proctor/server/store/timerlayer"
)

type releaseStore struct {
	store.Store
	events *[]string
}

func TestStartupInfrastructureLogsMigrationAndStoreCache(t *testing.T) {
	t.Parallel()

	logger, err := logging.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = logger.Shutdown() })
	var output logging.Buffer
	require.NoError(t, logger.Configure(logging.Config{
		MaxFieldBytes: 1024,
		Targets: []logging.Target{{
			Name: "test", Type: "console", Level: "info", Format: "json", Writer: &output,
		}},
	}))
	logStartupInfrastructure(config.Default(), logger, &sqlstore.MigrationResult{
		Applied:       2,
		SchemaVersion: 7,
	})
	for _, value := range []string{
		"database migrations complete",
		`"applied":2`,
		`"schema_version":7`,
		"store cache ready",
		`"backend":"memory_lru"`,
	} {
		require.Contains(t, output.String(), value, "startup infrastructure log is missing")
	}
}

func (s releaseStore) Close() error {
	if s.events != nil {
		*s.events = append(*s.events, "store")
	}
	return nil
}

type releaseCache struct {
	platform.Cache
	events *[]string
}

func (c releaseCache) Close() error {
	if c.events != nil {
		*c.events = append(*c.events, "cache")
	}
	return nil
}

type releaseMailer struct {
	platform.Mailer
	events *[]string
}

func (m releaseMailer) Close() error {
	if m.events != nil {
		*m.events = append(*m.events, "mailer")
	}
	return nil
}

type releaseCluster struct {
	platform.Cluster
	events *[]string
}

func (c releaseCluster) NodeID() string                                       { return "release-node" }
func (c releaseCluster) RegisterHandler(cluster.Event, cluster.Handler) error { return nil }
func (c releaseCluster) Broadcast(context.Context, *cluster.Message) error    { return nil }

func (c releaseCluster) Stop(context.Context) error {
	if c.events != nil {
		*c.events = append(*c.events, "cluster")
	}
	return nil
}

type releaseVFS struct {
	vfs.FileSystem
	events *[]string
}

func (f releaseVFS) Close() error {
	if f.events != nil {
		*f.events = append(*f.events, "vfs")
	}
	return nil
}

func TestOwnedInfrastructureReleasesInReverseDependencyOrder(t *testing.T) {
	t.Parallel()

	events := []string{}
	owned := ownedInfrastructure{resources: platform.OwnedResources{
		Persistence: releaseStore{events: &events},
		Cache:       releaseCache{events: &events},
		Mailer:      releaseMailer{events: &events},
		VFS:         releaseVFS{events: &events},
		Cluster:     releaseCluster{events: &events},
	}}
	require.NoError(t, owned.release())
	want := []string{"cluster", "vfs", "mailer", "cache", "store"}
	require.Len(t, events, len(want), "release order")
	for index := range want {
		require.Equal(t, want[index], events[index], "release order = %v, want %v", events, want)
	}
}

type layerOrderStore struct {
	store.Store
	periods store.AcademicPeriodStore
}

func (s layerOrderStore) AcademicPeriod() store.AcademicPeriodStore   { return s.periods }
func (layerOrderStore) ClusterDiscovery() store.ClusterDiscoveryStore { return nil }
func (layerOrderStore) PluginSettings() store.PluginSettingsStore     { return nil }
func (layerOrderStore) Close() error                                  { return nil }

type layerOrderPeriods struct {
	store.AcademicPeriodStore
	period *model.AcademicPeriod
	reads  atomic.Int64
}

func (s *layerOrderPeriods) Get(context.Context, model.AcademicPeriodID) (*model.AcademicPeriod, error) {
	s.reads.Add(1)
	copy := *s.period
	return &copy, nil
}

func TestRootComposesLocalCacheOutsideTiming(t *testing.T) {
	t.Parallel()

	configuration, err := config.NewStore(context.Background(), config.NewMemoryStore(nil), config.StoreOptions{
		LookupEnv: func(string) (string, bool) { return "", false },
	})
	require.NoError(t, err)
	logger, err := logging.New()
	require.NoError(t, err)
	period := &model.AcademicPeriod{
		ID: model.NewAcademicPeriodID(), Owner: model.NewInstitutionAcademicPeriodOwner(model.NewInstitutionID()),
		Name: "2026", DisplayName: "2026", StartsAt: time.Now().UTC(),
		EndsAt: time.Now().UTC().Add(time.Hour), CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(), Revision: 1,
	}
	periods := &layerOrderPeriods{period: period}
	cache, err := newStoreLocalMemoryCache(8)
	require.NoError(t, err)
	var timedReads atomic.Int64
	owner, err := openRuntimeInfrastructure(context.Background(), "", TestingOverrides{
		Configuration: configuration,
		Logger:        logger,
		Persistence:   layerOrderStore{periods: periods},
		Cache:         releaseCache{}, Mailer: releaseMailer{}, Filesystem: releaseVFS{}, Cluster: releaseCluster{},
		StoreLocalCache: cache,
		StoreMetrics: timerlayer.RecorderFunc(func(operation timerlayer.Operation, _ timerlayer.Outcome, _ time.Duration) {
			if operation.String() == "academic_period.get" {
				timedReads.Add(1)
			}
		}),
	}, schemaMigrate)
	require.NoError(t, err)
	t.Cleanup(func() { _ = owner.release() })
	for range 2 {
		_, err := owner.resources.Persistence.AcademicPeriod().Get(context.Background(), period.ID)
		require.NoError(t, err)
	}
	require.Equal(t, int64(1), periods.reads.Load(), "authoritative reads")
	require.Equal(t, int64(1), timedReads.Load(), "timed reads")
}

func TestRootSelectsLocalDevelopmentInfrastructure(t *testing.T) {
	t.Parallel()

	settings := config.Default()
	settings.VFS.Local.Root = t.TempDir()
	logger, err := logging.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = logger.Shutdown() })

	cache, err := newCache(settings.Cache)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cache.Close() })
	require.NoError(t, cache.Ping(context.Background()))

	mailer, err := newMailer(settings.Mail)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mailer.Close() })
	require.False(t, mailer.Enabled(), "disabled mail configuration selected an enabled mailer")

	filesystem, err := newVFS(settings.VFS)
	require.NoError(t, err)
	_, err = filesystem.List(context.Background(), vfs.ListOptions{Limit: 1})
	require.NoError(t, err)

	cluster, err := newCluster(settings.Cluster, logger, nil, "test", nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cluster.Stop(context.Background()) })
	require.Equal(t, settings.Cluster.NodeID, cluster.NodeID(), "cluster node ID")

	plugins, _, err := activatePlugins(context.Background(), settings, nil, logger, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = plugins.Close(context.Background()) })
	providers, err := newExternalAuthenticationRegistry(plugins)
	require.NoError(t, err)
	require.NoError(t, providers.Configure(settings.Authentication.External))
}

func TestRootSelectsConfiguredSMTPVFSAndIdentityAdapters(t *testing.T) {
	t.Parallel()

	settings := config.Default()

	settings.Mail.Enabled = true
	settings.Mail.Backend = "smtp"
	mailer, err := newMailer(settings.Mail)
	require.NoError(t, err)
	require.True(t, mailer.Enabled(), "enabled SMTP configuration selected a disabled mailer")

	settings.VFS.Backend = "s3"
	settings.VFS.S3.Endpoint = "s3.example.test"
	settings.VFS.S3.Bucket = "proctor"
	_, err = newVFS(settings.VFS)
	require.NoError(t, err)

	logger, err := logging.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = logger.Shutdown() })
	plugins, _, err := activatePlugins(context.Background(), settings, nil, logger, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = plugins.Close(context.Background()) })
	providers, err := newExternalAuthenticationRegistry(plugins)
	require.NoError(t, err)
	external := settings.Authentication.External
	external.Providers = []config.ExternalAuthenticationProvider{
		{
			ID: "campus-cas", Type: config.ExternalAuthenticationTypeCAS,
			DisplayName: "Campus CAS", Enabled: true,
			CAS: &config.CASProvider{
				BaseURL: "https://cas.example.test/cas", ValidationPath: "/p3/serviceValidate",
				Timeout: config.Duration{Duration: 5 * time.Second}, MaxResponseBytes: 64 << 10,
			},
			Claims: config.ExternalClaimMapping{Subject: "user"},
		},
		{
			ID: "campus-oidc", Type: config.ExternalAuthenticationTypeOIDC,
			DisplayName: "Campus OIDC", Enabled: true,
			OIDC: &config.OIDCProvider{
				Issuer: "https://oidc.example.test", ClientID: "proctor",
				Scopes: []string{"openid"}, Timeout: config.Duration{Duration: 5 * time.Second},
				MaxResponseBytes: 64 << 10,
			},
			Claims: config.ExternalClaimMapping{Subject: "sub"},
		},
	}
	require.NoError(t, providers.Configure(external))
	require.Len(t, providers.Descriptors(), 2)
}

func TestActivePluginSummariesListOnlyActivePlugins(t *testing.T) {
	t.Parallel()

	logger, err := logging.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = logger.Shutdown() })
	saved := savedPluginSettings{rows: []*model.PluginSettings{pluginSettingsRow(t, "cas", false)}}
	plugins, _, err := activatePlugins(context.Background(), config.Default(), saved, logger, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = plugins.Close(context.Background()) })

	require.Equal(t, []httpapi.PluginSummary{
		{ID: "oidc", Name: "OpenID Connect"},
		{ID: "export.zip", Name: "Zip export"},
	}, pluginDirectory{host: plugins}.Active(), "the disabled CAS plugin is not listed")
	require.False(t, pluginDirectory{host: plugins}.Degraded(), "a disabled plugin does not degrade readiness")
}

// eventPlugin subscribes to one topic, or fails to activate.
type eventPlugin struct {
	id          string
	activateErr error
}

func (p eventPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: p.id, Name: "Event " + p.id, EnabledByDefault: true}
}

func (p eventPlugin) Activate(_ context.Context, host plugin.Host) error {
	if p.activateErr != nil {
		return p.activateErr
	}
	return host.AddEventHandler(plugin.TopicExamAttemptSubmitted, eventHandlerFunc(func(context.Context, plugin.Event) error { return nil }))
}

func (eventPlugin) Deactivate(context.Context) error { return nil }

type eventHandlerFunc func(context.Context, plugin.Event) error

func (f eventHandlerFunc) HandleEvent(ctx context.Context, event plugin.Event) error {
	return f(ctx, event)
}

func TestPluginEventSubscriptionsAndDegradedReadiness(t *testing.T) {
	t.Parallel()

	logger, err := logging.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = logger.Shutdown() })
	plugins, _, err := activatePlugins(context.Background(), config.Default(), nil, logger, nil, []plugin.Plugin{
		eventPlugin{id: "mirror"},
		eventPlugin{id: "broken", activateErr: errors.New("unreachable")},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = plugins.Close(context.Background()) })

	subscriptions := eventSubscriptions(plugins)
	require.Len(t, subscriptions, 1, "only the active plugin subscribes")
	require.Equal(t, "mirror:exam.attempt.submitted", subscriptions[0].Key, "the key names the plugin and topic")
	require.Equal(t, plugin.TopicExamAttemptSubmitted, subscriptions[0].Topic)
	require.True(t, pluginDirectory{host: plugins}.Degraded(), "a failed plugin degrades readiness")
}

// pluginSettingsRow is a saved row turning plugin id on or off.
func pluginSettingsRow(t *testing.T, id model.PluginID, enabled bool) *model.PluginSettings {
	t.Helper()
	row, err := model.NewPluginSettings(id, model.PluginSettingsChange{Enabled: enabled, Settings: json.RawMessage(`{}`)}, time.Now())
	require.NoError(t, err)
	return row
}

// savedPluginSettings is a fixed list of saved plugin settings.
type savedPluginSettings struct {
	store.PluginSettingsStore
	rows []*model.PluginSettings
}

func (s savedPluginSettings) List(context.Context) ([]*model.PluginSettings, error) {
	return s.rows, nil
}
func (s savedPluginSettings) ChangeCount(context.Context) (int64, error) {
	return int64(len(s.rows)), nil
}
