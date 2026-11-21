// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/app/exam/assistant"
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

// FileContent is the full file content adapter. Only New needs all of it;
// each workflow receives a narrower interface of its own.
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

// Dependencies are the capabilities the server package supplies to New. App
// never holds platform.Service.
type Dependencies struct {
	Store                store.Catalog
	Cache                identity.AuthenticationCache
	MailDeliverySender   mail.Sender
	MailTemplateRenderer *mail.TemplateRenderer
	MailDeliveryRecorder mail.DeliveryRecorder
	MailMetricsReader    mail.MetricsReader
	JobRecorder          job.Recorder
	// JobTracer, when set, encloses each claimed job execution in a span.
	JobTracer           job.Tracer
	OperationalRecorder operational.Recorder
	// MFASecretSealer seals TOTP secrets; nil until the MFA ring is
	// configured.
	MFASecretSealer *secretseal.Sealer
	// MailSecretSealer seals recoverable mail payloads; nil until its ring is
	// configured.
	MailSecretSealer *secretseal.Sealer
	Registry         identity.ExternalProviderSource
	FileContent      FileContent
	// Plugins nil makes plugin administration unavailable.
	Plugins administration.PluginCatalog
	// DesktopBuildCatalog is the verified build catalog embedded in the
	// release. An empty catalog admits no released builds.
	DesktopBuildCatalog []model.DesktopBuildTuple
	// SkipDesktopCompatibility relaxes the login build check for development.
	// Protected attempt admission always requires a verified build.
	SkipDesktopCompatibility bool

	NodeID    string
	PublicURL string
	// ExecutionEnabled is whether exams may publish with an execution
	// environment.
	ExecutionEnabled   bool
	AssistantEnabled   bool
	AssistantProviders []string
	// AssistantGateway reaches the configured model providers. Nil, as when
	// the assistant is off, builds no assistant.
	AssistantGateway assistant.ModelGateway
	AssistantLimits  assistant.Limits
	// EnvironmentWaker wakes an attempt environment's owner after its facts
	// or workspace change. Nil wakes nobody; the runtime's backstop still
	// finds the change.
	EnvironmentWaker environment.Waker
	// EnvironmentSettles delivers completed settles, which a seal waits on.
	// Nil settles nothing before a seal.
	EnvironmentSettles *environment.ClusterSettles
	// EnvironmentDiagnostics receives environment failures nobody else sees,
	// such as a settle that could not be asked. Nil drops them.
	EnvironmentDiagnostics func(model.ExamAttemptID, error)
	// EnvironmentHosts lets any node serve a candidate's terminals. Nil
	// serves none.
	EnvironmentHosts environment.Hosts
	// KillSealedAssistantSessions stops the committed seal's remaining PTYs.
	KillSealedAssistantSessions func(context.Context, model.ExamAttemptID, []string) error
	// SupportAddress is the help mailbox public discovery publishes; empty
	// publishes none.
	SupportAddress string
	// LoopbackHTTPDevelopment marks explicit local development. It never
	// relaxes non-loopback issuer rules.
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
