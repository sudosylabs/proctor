// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/redis/rueidis"

	"github.com/sudosylabs/proctor/packages/cache"
	"github.com/sudosylabs/proctor/packages/cache/memory"
	"github.com/sudosylabs/proctor/packages/cache/redis"
	"github.com/sudosylabs/proctor/packages/mail"
	"github.com/sudosylabs/proctor/packages/mail/smtp"
	"github.com/sudosylabs/proctor/packages/vfs"
	localvfs "github.com/sudosylabs/proctor/packages/vfs/local"
	"github.com/sudosylabs/proctor/packages/vfs/s3"
	"github.com/sudosylabs/proctor/server/app"
	"github.com/sudosylabs/proctor/server/cluster"
	"github.com/sudosylabs/proctor/server/cluster/local"
	"github.com/sudosylabs/proctor/server/cluster/memberlist"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/metrics"
	"github.com/sudosylabs/proctor/server/platform"
	"github.com/sudosylabs/proctor/server/platform/externalauth"
	"github.com/sudosylabs/proctor/server/platform/externalauth/cas"
	"github.com/sudosylabs/proctor/server/platform/externalauth/oidc"
	"github.com/sudosylabs/proctor/server/pluginhost"
	"github.com/sudosylabs/proctor/server/store"
	"github.com/sudosylabs/proctor/server/store/localcachelayer"
	"github.com/sudosylabs/proctor/server/store/retrylayer"
	"github.com/sudosylabs/proctor/server/store/sqlstore"
	"github.com/sudosylabs/proctor/server/store/timerlayer"
	"github.com/sudosylabs/proctor/server/tracing"
)

const (
	storeLocalCacheMaxEntries = 1_024
	storeLocalCacheMaxBytes   = 64 << 20
)

// schemaPolicy selects how construction treats the database schema.
type schemaPolicy uint8

const (
	// schemaMigrate applies pending forward migrations, as normal serving does.
	schemaMigrate schemaPolicy = iota
	// schemaRequireCurrent refuses any schema that differs from this build.
	schemaRequireCurrent
)

type schemaPersistence interface {
	ApplyPendingMigrations(context.Context) (sqlstore.MigrationResult, error)
	ValidateSchema(context.Context) error
}

// prepareSchema converges or verifies the schema. A required-current check
// never writes: it reports a behind or ahead schema and points the operator
// at the explicit migrate command.
func prepareSchema(ctx context.Context, persistence schemaPersistence, policy schemaPolicy) (*sqlstore.MigrationResult, error) {
	if policy == schemaRequireCurrent {
		if err := persistence.ValidateSchema(ctx); err != nil {
			return nil, fmt.Errorf(
				"database schema is not current; this command never migrates, so run `proctor migrate status` and `proctor migrate up` with every node stopped, then retry: %w",
				err,
			)
		}

		return nil, nil
	}

	migration, err := persistence.ApplyPendingMigrations(ctx)
	if err != nil {
		return nil, fmt.Errorf("migrate database: %w", err)
	}

	return &migration, nil
}

// ownedInfrastructure is the sole owner of infrastructure while the root is
// acquiring and decorating it. resources is exactly what transfers to the
// Platform boundary, as one unit; the metrics and tracing modules stay with
// the root, which closes them itself. Before the transfer, release is the
// only cleanup path.
type ownedInfrastructure struct {
	resources platform.OwnedResources
	migration *sqlstore.MigrationResult
	metrics   *metrics.Module
	tracing   *tracing.Module
	// plugins are activated right after configuration and logging, so that
	// what they register is ready before anything that uses it is built.
	plugins *pluginhost.Host
}

// constructionCapabilities is a short-lived, non-owning projection used only
// while the root assembles consumers. It has no lifecycle operations and must
// not escape composeNode; Platform owns every referenced capability after
// acceptance.
type constructionCapabilities struct {
	logger                 *logging.Logger
	persistence            store.Catalog
	cache                  borrowedCache
	cluster                borrowedCluster
	mailer                 borrowedMailer
	filesystem             vfs.FileSystem
	externalAuthentication *externalauth.Registry
	nodeID                 string
	migration              *sqlstore.MigrationResult
	metrics                *metrics.Module
	tracing                *tracing.Module
	// plugins, like metrics and tracing, passes to compositionRelease and
	// then to the Server, which deactivates it.
	plugins *pluginhost.Host
}

type borrowedCache interface {
	Get(context.Context, string) ([]byte, error)
	Set(context.Context, string, []byte, time.Duration, platform.CacheCondition) error
	Delete(context.Context, string) error
	Add(context.Context, string, int64, time.Duration) (int64, error)
}

type borrowedMailer interface {
	Enabled() bool
	From() mail.Address
	Send(context.Context, mail.Message) (mail.Receipt, error)
	Test(context.Context) error
}

type borrowedCluster interface {
	RegisterHandler(cluster.Event, cluster.Handler) error
	Broadcast(context.Context, *cluster.Message) error
}

func openRuntimeInfrastructure(
	ctx context.Context,
	configPath string,
	overrides TestingOverrides,
	schema schemaPolicy,
) (result ownedInfrastructure, resultErr error) {
	result = ownedInfrastructure{resources: platform.OwnedResources{
		Configuration: overrides.Configuration,
		Logger:        overrides.Logger,
		Persistence:   overrides.Persistence,
		Cache:         overrides.Cache,
		Cluster:       overrides.Cluster,
		Mailer:        overrides.Mailer,
		VFS:           overrides.Filesystem,
	}}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, result.release())
		}
	}()

	if err := checkAcquisitionContext(ctx, "begin infrastructure acquisition"); err != nil {
		return result, err
	}
	if err := result.acquireConfigurationAndLogger(ctx, configPath); err != nil {
		return result, err
	}
	cfg := result.resources.Configuration.Get()
	plugins, pluginsErr := activatePlugins(ctx, cfg, result.resources.Logger, overrides.Plugins)
	if pluginsErr != nil {
		return result, pluginsErr
	}
	result.plugins = plugins
	if err := result.acquirePersistence(ctx, cfg, schema); err != nil {
		return result, err
	}
	metricsModule, tracingModule, err := result.constructTelemetry(cfg)
	if err != nil {
		return result, err
	}
	if err = result.decoratePersistenceWithRetryAndTiming(ctx, cfg, overrides, metricsModule, tracingModule); err != nil {
		return result, err
	}
	if err = result.acquireCacheMailerAndVFS(ctx, cfg, metricsModule); err != nil {
		return result, err
	}
	if err = result.acquireCluster(ctx, cfg, metricsModule); err != nil {
		return result, err
	}
	if err = result.decoratePersistenceWithLocalCache(ctx, overrides, metricsModule); err != nil {
		return result, err
	}
	externalAuthentication, err := newExternalAuthenticationRegistry()
	if err != nil {
		return result, fmt.Errorf("construct external authentication registry: %w", err)
	}
	result.resources.ExternalAuthentication = externalAuthentication
	if err := checkAcquisitionContext(ctx, "acquire external authentication registry"); err != nil {
		return result, err
	}
	return result, nil
}

// acquireConfigurationAndLogger opens the configuration and then the logger unless overrides supplied them.
func (i *ownedInfrastructure) acquireConfigurationAndLogger(ctx context.Context, configPath string) error {
	if i.resources.Configuration == nil {
		configuration, err := config.Open(ctx, configPath)
		if err != nil {
			return fmt.Errorf("acquire configuration: %w", err)
		}
		i.resources.Configuration = configuration
	}
	if err := checkAcquisitionContext(ctx, "acquire configuration"); err != nil {
		return err
	}

	if i.resources.Logger == nil {
		logger, err := logging.New()
		if err != nil {
			return fmt.Errorf("create logger: %w", err)
		}
		i.resources.Logger = logger
	}
	i.resources.Logger.SetContextFields(logCorrelationFields)
	return checkAcquisitionContext(ctx, "acquire logger")
}

// acquirePersistence opens the SQL store and prepares its schema unless overrides supplied a store.
func (i *ownedInfrastructure) acquirePersistence(ctx context.Context, cfg config.Config, schema schemaPolicy) error {
	if i.resources.Persistence == nil {
		persistence, err := sqlstore.New(ctx, sqlstore.SettingsFromConfig(cfg.Database))
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		migration, err := prepareSchema(ctx, persistence, schema)
		if err != nil {
			return errors.Join(err, persistence.Close())
		}
		i.migration = migration
		i.resources.Persistence = persistence
	}
	return checkAcquisitionContext(ctx, "acquire persistence")
}

// constructTelemetry builds the metrics and then the tracing module over the acquired store and logger and records each for release.
func (i *ownedInfrastructure) constructTelemetry(cfg config.Config) (*metrics.Module, *tracing.Module, error) {
	var databaseStats func() sql.DBStats
	if source, ok := i.resources.Persistence.(interface{ Stats() sql.DBStats }); ok {
		databaseStats = source.Stats
	}

	currentBuild := app.CurrentBuildInfo()
	metricsModule, err := metrics.New(cfg.Metrics, metrics.BuildInfo{
		Version:   currentBuild.Version,
		Commit:    currentBuild.Commit,
		GoVersion: currentBuild.GoVersion,
	}, metrics.Sources{
		Database: databaseStats,
		ErrorLog: i.resources.Logger.StdLogger(slog.LevelError),
		Logging: func() metrics.LogStats {
			stats := i.resources.Logger.Stats()
			return metrics.LogStats{Dropped: stats.Dropped, InternalErrors: stats.InternalErrors}
		},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("construct metrics module: %w", err)
	}

	i.metrics = metricsModule
	tracingModule, err := tracing.New(cfg.Tracing, tracing.Identity{
		Version: currentBuild.Version,
		NodeID:  cfg.Cluster.NodeID,
	}, i.resources.Logger.StdLogger(slog.LevelError))
	if err != nil {
		return nil, nil, fmt.Errorf("construct tracing module: %w", err)
	}

	i.tracing = tracingModule
	return metricsModule, tracingModule, nil
}

// decoratePersistenceWithRetryAndTiming wraps the store in the retry layer and then the timing layer.
func (i *ownedInfrastructure) decoratePersistenceWithRetryAndTiming(
	ctx context.Context,
	cfg config.Config,
	overrides TestingOverrides,
	metricsModule *metrics.Module,
	tracingModule *tracing.Module,
) error {
	storeRetry := retrylayer.DefaultPolicy(sqlstore.IsTransientError)
	if overrides.StoreRetry != nil {
		storeRetry = *overrides.StoreRetry
	}

	var storeRetryMetrics retrylayer.Recorder
	if metricsModule.Enabled() {
		storeRetryMetrics = metricsModule
	}

	retriedPersistence, err := retrylayer.NewWithRecorder(i.resources.Persistence, storeRetry, storeRetryMetrics)
	if err != nil {
		return fmt.Errorf("construct store retry layer: %w", err)
	}

	i.resources.Persistence = retriedPersistence
	if checkErr := checkAcquisitionContext(ctx, "decorate persistence with retry"); checkErr != nil {
		return checkErr
	}

	storeMetrics := overrides.StoreMetrics
	if storeMetrics == nil {
		if metricsModule.Enabled() {
			storeMetrics = metricsModule
		} else {
			storeMetrics = timerlayer.NopRecorder{}
		}
	}

	timerOptions := timerlayer.Options{
		SlowThreshold: cfg.Database.SlowOperationThreshold.Duration,
		Slow:          slowStoreOperationLog{logger: i.resources.Logger},
	}
	if tracingModule.Enabled() {
		timerOptions.Tracer = storeTracing{tracing: tracingModule}
	}

	timedPersistence, err := timerlayer.New(i.resources.Persistence, storeMetrics, timerOptions)
	if err != nil {
		return fmt.Errorf("construct store timing layer: %w", err)
	}

	i.resources.Persistence = timedPersistence
	return checkAcquisitionContext(ctx, "decorate persistence with timing")
}

// acquireCacheMailerAndVFS opens the cache, mail transport and VFS in order, measuring each when metrics are enabled.
func (i *ownedInfrastructure) acquireCacheMailerAndVFS(ctx context.Context, cfg config.Config, metricsModule *metrics.Module) error {
	if i.resources.Cache == nil {
		cache, err := newCache(cfg.Cache)
		if err != nil {
			return fmt.Errorf("open cache: %w", err)
		}

		i.resources.Cache = cache
	}

	if metricsModule.Enabled() {
		i.resources.Cache = metrics.WrapCache(i.resources.Cache, cfg.Cache.Backend, metricsModule)
	}
	if err := checkAcquisitionContext(ctx, "acquire cache"); err != nil {
		return err
	}

	if i.resources.Mailer == nil {
		mailer, err := newMailer(cfg.Mail)
		if err != nil {
			return fmt.Errorf("open mail transport: %w", err)
		}

		i.resources.Mailer = mailer
	}

	if metricsModule.Enabled() {
		i.resources.Mailer = metrics.WrapMailer(i.resources.Mailer, metricsModule)
	}
	if err := checkAcquisitionContext(ctx, "acquire mail transport"); err != nil {
		return err
	}

	if i.resources.VFS == nil {
		filesystem, err := newVFS(cfg.VFS)
		if err != nil {
			return fmt.Errorf("open VFS: %w", err)
		}

		i.resources.VFS = filesystem
	}

	if metricsModule.Enabled() {
		i.resources.VFS = metrics.WrapVFS(i.resources.VFS, cfg.VFS.Backend, metricsModule)
	}

	return checkAcquisitionContext(ctx, "acquire VFS")
}

// acquireCluster opens the cluster transport unless overrides supplied one, measuring it when metrics are enabled.
func (i *ownedInfrastructure) acquireCluster(ctx context.Context, cfg config.Config, metricsModule *metrics.Module) error {
	if i.resources.Cluster == nil {
		var discovery store.ClusterDiscoveryStore
		if i.resources.Persistence != nil {
			discovery = i.resources.Persistence.ClusterDiscovery()
		}
		var clusterMetrics cluster.Metrics
		if metricsModule.Enabled() {
			clusterMetrics = metricsModule
		}
		cluster, err := newCluster(cfg.Cluster, i.resources.Logger, discovery, app.Version, clusterMetrics)
		if err != nil {
			return fmt.Errorf("open cluster transport: %w", err)
		}
		i.resources.Cluster = cluster
	}
	if metricsModule.Enabled() {
		if peers, ok := i.resources.Cluster.(interface{ PeerCount() int }); ok {
			metricsModule.SetClusterPeerSource(peers.PeerCount)
		}
		i.resources.Cluster = metrics.WrapCluster(i.resources.Cluster, metricsModule)
	}
	return checkAcquisitionContext(ctx, "acquire cluster transport")
}

// decoratePersistenceWithLocalCache wraps the store in the cluster-invalidated local cache layer.
func (i *ownedInfrastructure) decoratePersistenceWithLocalCache(
	ctx context.Context,
	overrides TestingOverrides,
	metricsModule *metrics.Module,
) error {
	storeLocalCache := overrides.StoreLocalCache
	if storeLocalCache == nil {
		var err error
		storeLocalCache, err = newStoreLocalMemoryCache(storeLocalCacheMaxEntries)
		if err != nil {
			return fmt.Errorf("construct local store cache: %w", err)
		}
	}

	storeCachePolicy := localcachelayer.DefaultPolicy()
	if overrides.StoreCachePolicy != nil {
		storeCachePolicy = *overrides.StoreCachePolicy
	}

	storeCacheMetrics := overrides.StoreCacheMetrics
	if storeCacheMetrics == nil {
		if metricsModule.Enabled() {
			storeCacheMetrics = metricsModule.StoreCacheRecorder()
		} else {
			storeCacheMetrics = localcachelayer.NopRecorder{}
		}
	}

	cacheInvalidation, err := newLocalCacheClusterAdapter(i.resources.Cluster)
	if err != nil {
		return fmt.Errorf("construct local-cache invalidation adapter: %w", err)
	}

	cachedPersistence, err := localcachelayer.New(
		i.resources.Persistence,
		storeLocalCache,
		storeCachePolicy,
		storeCacheMetrics,
		cacheInvalidation,
	)
	if err != nil {
		return fmt.Errorf("construct local-cache store layer: %w", err)
	}

	i.resources.Persistence = cachedPersistence
	return checkAcquisitionContext(ctx, "decorate persistence with local cache")
}

func newStoreLocalMemoryCache(maxEntries int) (localcachelayer.Cache, error) {
	store, err := memory.New(cache.BytesCodec(), memory.Config{
		MaxEntries: maxEntries,
		MaxBytes:   storeLocalCacheMaxBytes,
	})
	if err != nil {
		return nil, err
	}
	return localcachelayer.NewCacheAdapter(store)
}

func checkAcquisitionContext(ctx context.Context, phase string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s: %w", phase, err)
	}
	return nil
}

func logStartupInfrastructure(
	snapshot config.Config,
	logger *logging.Logger,
	migration *sqlstore.MigrationResult,
) {
	if logger == nil {
		return
	}
	if migration != nil {
		logger.Info(
			"database migrations complete",
			logging.Int("applied", migration.Applied),
			logging.Int("schema_version", migration.SchemaVersion),
		)
	}
	logger.Info(
		"store cache ready",
		logging.String("backend", "memory_lru"),
		logging.Int("max_entries", storeLocalCacheMaxEntries),
		logging.Int64("max_bytes", storeLocalCacheMaxBytes),
		logging.String("invalidation_backend", snapshot.Cluster.Backend),
	)
	logger.Info(
		"metrics configured",
		logging.Bool("enabled", snapshot.Metrics.Enabled),
		logging.String("listen_address", snapshot.Metrics.ListenAddress),
		logging.Bool("tls", snapshot.Metrics.TLS.CertificateFile != ""),
		logging.Bool("bearer_authentication", snapshot.Metrics.BearerToken != ""),
		logging.Bool("profiling", snapshot.Metrics.ProfilingEnabled),
	)
}

func (i *ownedInfrastructure) acceptPlatform(
	ctx context.Context,
) (*platform.Service, config.Config, constructionCapabilities, error) {
	resources := i.resources
	capabilities := constructionCapabilities{
		logger:                 resources.Logger,
		persistence:            resources.Persistence,
		cache:                  resources.Cache,
		cluster:                resources.Cluster,
		mailer:                 resources.Mailer,
		filesystem:             resources.VFS,
		externalAuthentication: resources.ExternalAuthentication,
		migration:              i.migration,
		metrics:                i.metrics,
		tracing:                i.tracing,
		plugins:                i.plugins,
	}
	if resources.Cluster != nil {
		capabilities.nodeID = resources.Cluster.NodeID()
	}

	shutdownTimeout := i.shutdownTimeout()
	*i = ownedInfrastructure{}
	service, snapshot, err := platform.Accept(ctx, resources)
	if err != nil {
		// Metrics and tracing share one shutdown budget, as in release.
		budget, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return nil, config.Config{}, constructionCapabilities{}, errors.Join(
			err,
			capabilities.plugins.Close(budget),
			capabilities.metrics.Close(budget),
			capabilities.tracing.Close(budget),
		)
	}

	return service, snapshot, capabilities, nil
}

// shutdownTimeout is the configured Server.ShutdownTimeout, or its default
// when no configuration has been loaded.
func (i *ownedInfrastructure) shutdownTimeout() time.Duration {
	if i.resources.Configuration != nil {
		return i.resources.Configuration.Get().Server.ShutdownTimeout.Duration
	}
	return 15 * time.Second
}

func (i *ownedInfrastructure) release() error {
	stopCtx, cancelStop := context.WithTimeout(context.Background(), i.shutdownTimeout())
	defer cancelStop()
	// Plugins deactivate first: they may still use the infrastructure below.
	pluginsErr := i.plugins.Close(stopCtx)

	var clusterErr error
	if i.resources.Cluster != nil {
		clusterErr = i.resources.Cluster.Stop(stopCtx)
	}

	var vfsErr error
	if closer, ok := i.resources.VFS.(interface{ Close() error }); ok {
		vfsErr = closer.Close()
	}

	var mailErr error
	if i.resources.Mailer != nil {
		mailErr = i.resources.Mailer.Close()
	}

	var cacheErr error
	if i.resources.Cache != nil {
		cacheErr = i.resources.Cache.Close()
	}

	var storeErr error
	if i.resources.Persistence != nil {
		storeErr = i.resources.Persistence.Close()
	}

	var loggerErr error
	if i.resources.Logger != nil {
		loggerErr = i.resources.Logger.Shutdown(stopCtx)
	}

	var configErr error
	if i.resources.Configuration != nil {
		configErr = i.resources.Configuration.Close()
	}

	var metricsErr error
	if i.metrics != nil {
		metricsErr = i.metrics.Close(stopCtx)
	}

	var tracingErr error
	if i.tracing != nil {
		tracingErr = i.tracing.Close(context.Background())
	}

	return errors.Join(
		pluginsErr,
		metricsErr,
		tracingErr,
		clusterErr,
		vfsErr,
		mailErr,
		cacheErr,
		storeErr,
		loggerErr,
		configErr,
	)
}

func newCache(settings config.Cache) (platform.Cache, error) {
	switch settings.Backend {
	case "memory":
		store, err := memory.New(cache.BytesCodec(), memory.Config{
			MaxEntries:  settings.Memory.MaxEntries,
			MaxBytes:    settings.Memory.MaxBytes,
			MaxCounters: settings.Memory.MaxCounters,
		})
		if err != nil {
			return nil, err
		}

		return platform.NewCacheAdapter(platform.CacheBackend{Store: store}), nil
	case "redis":
		clientOption := rueidis.ClientOption{
			InitAddress: append([]string(nil), settings.Redis.Addresses...),
			Username:    settings.Redis.Username,
			Password:    settings.Redis.Password,
			SelectDB:    settings.Redis.Database,
			ClientName:  "proctor",
			Dialer: net.Dialer{
				Timeout:   settings.Redis.ConnectTimeout.Duration,
				KeepAlive: 30 * time.Second,
			},
		}
		if settings.Redis.TLS {
			clientOption.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		}

		client, err := rueidis.NewClient(clientOption)
		if err != nil {
			return nil, fmt.Errorf("create Redis client: %w", err)
		}

		store, err := redis.New(
			client,
			cache.BytesCodec(),
			redis.Config{Namespace: settings.Namespace},
		)
		if err != nil {
			client.Close()
			return nil, err
		}

		return platform.NewCacheAdapter(platform.CacheBackend{
			Store: store,
			Ping:  func(ctx context.Context) error { return client.Do(ctx, client.B().Ping().Build()).Error() },
			Close: func() error {
				client.Close()
				return nil
			},
		}), nil
	default:
		return nil, fmt.Errorf("unsupported cache backend %q", settings.Backend)
	}
}

func newMailer(settings config.Mail) (platform.Mailer, error) {
	from := mail.Address{Name: settings.FromName, Address: settings.FromAddress}
	if !settings.Enabled {
		return platform.NewDisabledMailer(from), nil
	}
	if settings.Backend != "smtp" {
		return nil, fmt.Errorf("unsupported mail backend %q", settings.Backend)
	}
	sender, err := smtp.New(smtp.Config{
		Address:         settings.SMTP.Address,
		ServerName:      settings.SMTP.ServerName,
		LocalName:       settings.SMTP.LocalName,
		Security:        smtp.Security(settings.SMTP.Security),
		Username:        settings.SMTP.Username,
		Password:        settings.SMTP.Password,
		Authentication:  smtp.Authentication(settings.SMTP.Authentication),
		Timeout:         settings.SMTP.Timeout.Duration,
		MessageIDDomain: settings.SMTP.MessageIDDomain,
		MaxMessageBytes: settings.SMTP.MaxMessageBytes,
		MaxRecipients:   settings.SMTP.MaxRecipients,
	})
	if err != nil {
		return nil, err
	}
	return platform.NewMailAdapter(true, from, sender), nil
}

func newVFS(settings config.VFS) (vfs.FileSystem, error) {
	switch settings.Backend {
	case "local":
		return localvfs.New(settings.Local.Root)
	case "s3":
		return s3.New(s3.Config{
			Endpoint:     settings.S3.Endpoint,
			AccessKey:    settings.S3.AccessKey,
			SecretKey:    settings.S3.SecretKey,
			SessionToken: settings.S3.SessionToken,
			Bucket:       settings.S3.Bucket,
			Prefix:       settings.S3.Prefix,
			Region:       settings.S3.Region,
			Secure:       settings.S3.Secure,
		})
	default:
		return nil, fmt.Errorf("unsupported VFS backend %q", settings.Backend)
	}
}

func newCluster(
	settings config.Cluster,
	logger *logging.Logger,
	discovery store.ClusterDiscoveryStore,
	serverVersion string,
	metrics cluster.Metrics,
) (cluster.Transport, error) {
	switch settings.Backend {
	case "local":
		return local.New(settings.NodeID, loggingClusterLogger{log: logger.With(
			logging.String("component", "cluster"),
			logging.String("node_id", settings.NodeID),
		)})
	case "memberlist":
		if discovery == nil {
			return nil, fmt.Errorf("cluster discovery store is required for memberlist")
		}

		key, decryptionKeys, err := memberlist.DecodeEncryptionKeyring(
			settings.Memberlist.EncryptionKey,
			settings.Memberlist.DecryptionKeys,
		)
		if err != nil {
			return nil, err
		}
		if serverVersion == "" {
			serverVersion = app.Version
		}

		return memberlist.New(memberlist.Config{
			NodeID:             settings.NodeID,
			BindAddress:        settings.Memberlist.BindAddress,
			AdvertiseAddress:   settings.Memberlist.AdvertiseAddress,
			EncryptionKey:      key,
			DecryptionKeys:     decryptionKeys,
			SeedAddresses:      append([]string(nil), settings.Memberlist.SeedAddresses...),
			Discovery:          storeClusterDiscovery{store: discovery},
			DiscoveryTTL:       settings.Memberlist.DiscoveryTTL.Duration,
			DiscoveryHeartbeat: settings.Memberlist.DiscoveryHeartbeat.Duration,
			ServerVersion:      serverVersion,
			AllowPublicBind:    settings.Memberlist.AllowPublicBind,
			Logger: loggingClusterLogger{log: logger.With(
				logging.String("component", "cluster"),
				logging.String("node_id", settings.NodeID),
				logging.String("backend", "memberlist"),
			)},
			Metrics: metrics,
		})
	default:
		return nil, fmt.Errorf("unsupported cluster backend %q", settings.Backend)
	}
}

func newExternalAuthenticationRegistry() (*externalauth.Registry, error) {
	return externalauth.NewRegistry(
		cas.NewFactory(),
		oidc.NewFactory(),
	)
}
