// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"time"

	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/exam/correction"
	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/app/exam/resource"
	"github.com/sudosylabs/proctor/server/app/exam/workspace"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/app/jobs"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/onboarding"
	"github.com/sudosylabs/proctor/server/app/operational"
	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/secretseal"
	"github.com/sudosylabs/proctor/server/store"
)

// FileContent is the bounded composition contract. Each workflow receives a
// narrower consumer-owned capability; only the application constructor needs
// the complete concrete content adapter surface.
type FileContent interface {
	user.PictureFiles
	jobs.FileRevisionContentPurger
	jobs.StarterWorkspaceObjectPurger
	jobs.AttemptWorkspaceObjectPurger
	jobs.RetentionContentPurger
	exam.ExportContent
	attempt.Content
	resource.FileContent
	correction.Content
	workspace.Content
	onboarding.OnboardingImportFiles
}

// Dependencies are the explicit capabilities package app needs. The module-root
// composition package builds these from deployment configuration and concrete
// adapters; App never holds platform.Service.
type Dependencies struct {
	Store                store.Catalog
	Cache                identity.AuthenticationCache
	MailDeliverySender   mail.Sender
	MailTemplateRenderer mail.Renderer
	MailDeliveryRecorder mail.DeliveryRecorder
	MailMetricsReader    mail.MetricsReader
	JobRecorder          job.Recorder
	// JobTracer, when set, encloses each claimed Job execution in a span.
	JobTracer           job.Tracer
	OperationalRecorder operational.Recorder
	// MFASecretSealer is the concrete in-process cryptographic module for TOTP
	// secrets. It is nil until the independent MFA ring is configured.
	MFASecretSealer *secretseal.Sealer
	// MailSecretSealer is the concrete in-process cryptographic module for
	// recoverable mail payloads. It is nil until an independent ring is configured.
	MailSecretSealer *secretseal.Sealer
	Registry         identity.ExternalProviderSource
	FileContent      FileContent
	// Plugins describes and validates the compiled-in plugins for plugin
	// administration; nil makes it unavailable.
	Plugins administration.PluginCatalog
	// DesktopBuildCatalog is the immutable verified build catalog embedded by
	// the server release. An empty catalog admits no released builds.
	DesktopBuildCatalog []model.DesktopBuildTuple
	// SkipDesktopCompatibility is the immutable development-environment policy
	// for login. Protected Attempt admission always requires a verified build.
	SkipDesktopCompatibility bool

	NodeID    string
	PublicURL string
	// ExecutionEnabled is whether Exams may publish with an execution
	// environment.
	ExecutionEnabled bool
	// EnvironmentWaker wakes the owner of an Attempt's execution environment
	// after its facts or Workspace change. Nil wakes nobody; the runtime's
	// backstop still finds the change.
	EnvironmentWaker environment.Waker
	// EnvironmentSettles delivers the settles owners complete, which a seal
	// waits on. Nil settles nothing before a seal.
	EnvironmentSettles environment.SettleSubscriber
	// EnvironmentDiagnostics receives execution environment failures nobody
	// else sees, such as a settle that could not be asked. Nil drops them.
	EnvironmentDiagnostics func(model.ExamAttemptID, error)
	// EnvironmentHosts reaches the execution hosts, so any node serves a
	// candidate's terminals. Nil serves none.
	EnvironmentHosts environment.Hosts
	// SupportAddress is the operator-configured help mailbox that public
	// discovery publishes for hosted pages; empty publishes none.
	SupportAddress string
	// LoopbackHTTPDevelopment is the immutable composition-owned projection of
	// explicit local development. It never relaxes non-loopback issuer rules.
	LoopbackHTTPDevelopment bool

	Password                identity.PasswordPolicy
	PasswordWorkRecorder    identity.PasswordWorkRecorder
	Sessions                identity.SessionPolicy
	LoginRateLimit          identity.LoginRateLimitPolicy
	BootstrapProtection     identity.BootstrapProtectionPolicy
	PersonalAccessToken     identity.PersonalAccessTokenPolicy
	AccountRecovery         identity.AccountRecoveryPolicy
	MFA                     identity.MFAPolicy
	ExternalAuth            identity.ExternalAuthenticationPolicy
	RecentAuthenticationTTL time.Duration

	AuthenticationDiagnostics *logging.Logger
	RealtimeDiagnostics       *logging.Logger
	RecoveryDiagnostics       *logging.Logger
}
