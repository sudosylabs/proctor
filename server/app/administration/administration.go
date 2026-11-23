// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package administration owns the institution administrator's access and
// retention controls: roles and bindings, the access policy and public access
// discovery, retention, and the audit listings. Counts is a separate small
// service whose counters are supplied by the services that own each resource,
// so it imports none of them.
package administration

import (
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/people"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Dependencies are the collaborators New distributes to the Service.
type Dependencies struct {
	AcademicUnits store.AcademicUnitStore
	Store         store.Catalog
	Authorizer    *access.Authorizer
	Auditor       *access.Auditor
	Capabilities  *identity.DeploymentCapabilities
	Mail          *mail.Composer
	Publisher     realtime.Publisher
	// CanonicalOrigin, SupportAddress and PasswordMinimumLength are published
	// by access discovery unchanged.
	CanonicalOrigin         string
	SupportAddress          string
	PasswordMinimumLength   int
	RecentAuthenticationTTL time.Duration
	// Sessions are the deployment session lifetimes that bound the policy's
	// session lengths.
	Sessions identity.SessionPolicy
	// Plugins nil makes plugin administration unavailable.
	Plugins            PluginCatalog
	AssistantProviders []string
	// AuditExportContent nil makes audit exports unavailable.
	AuditExportContent AuditExportContent
	Now                func() time.Time
}

// Service holds every administration use case.
type Service struct {
	roles             store.RoleStore
	roleBindings      store.RoleBindingStore
	users             store.UserStore
	institutions      store.InstitutionStore
	audits            store.AuditStore
	accessPolicies    store.AccessPolicyStore
	retentionPolicies store.RetentionPolicyStore
	retention         store.RetentionStore
	// classes names the unit of a Class whose teachers a binding changed.
	classes        store.ClassStore
	academicUnits  store.AcademicUnitStore
	pluginSettings store.PluginSettingsStore
	// environmentProfiles is nil without persistence, which makes profile
	// administration unavailable.
	environmentProfiles store.EnvironmentProfileStore
	assistantProfiles   store.AssistantProfileStore
	assistantProviders  []string
	// auditExports and auditExportContent are both needed for audit exports.
	auditExports       store.AuditExportStore
	auditExportContent AuditExportContent

	authorizer *access.Authorizer
	people     *people.Resolver
	// auditor fails the attempt of a mutation that does not commit; the store
	// completes a committed one.
	auditor      access.MutationAuditAdapter
	publisher    realtime.Publisher
	mail         *mail.Composer
	capabilities *identity.DeploymentCapabilities
	plugins      PluginCatalog

	canonicalOrigin         string
	supportAddress          string
	passwordMinimumLength   int
	recentAuthenticationTTL time.Duration
	// sessionLimits are derived in New from the deployment session lifetimes.
	sessionLimits model.SessionLengthLimits
	now           func() time.Time
}

// New refuses an invalid access discovery presentation and missing
// collaborators.
func New(deps Dependencies) (*Service, error) {
	if deps.Store == nil {
		return nil, errors.New("administration store catalog is required")
	}

	catalog := deps.Store
	parsed, err := url.Parse(deps.CanonicalOrigin)
	supportAddress := strings.ToLower(strings.TrimSpace(deps.SupportAddress))
	if deps.PasswordMinimumLength < 1 || (supportAddress != "" && !model.IsValidEmail(supportAddress)) {
		return nil, errors.New("access policy public presentation is invalid")
	}

	if catalog.AccessPolicy() == nil ||
		catalog.Institution() == nil ||
		deps.Authorizer == nil ||
		deps.Capabilities == nil ||
		deps.Auditor == nil ||
		deps.Publisher == nil ||
		deps.Now == nil ||
		deps.RecentAuthenticationTTL <= 0 ||
		err != nil ||
		parsed.Scheme == "" ||
		parsed.Host == "" ||
		parsed.Path != "" ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return nil, errors.New("access policy service dependencies are invalid")
	}

	if catalog.RetentionPolicy() == nil || catalog.Retention() == nil {
		return nil, errors.New("retention policy service dependencies are invalid")
	}

	return &Service{
		roles:                   catalog.Role(),
		roleBindings:            catalog.RoleBinding(),
		users:                   catalog.User(),
		institutions:            catalog.Institution(),
		audits:                  catalog.Audit(),
		accessPolicies:          catalog.AccessPolicy(),
		retentionPolicies:       catalog.RetentionPolicy(),
		pluginSettings:          catalog.PluginSettings(),
		environmentProfiles:     catalog.EnvironmentProfile(),
		assistantProfiles:       catalog.AssistantProfile(),
		assistantProviders:      append([]string(nil), deps.AssistantProviders...),
		auditExports:            catalog.AuditExport(),
		auditExportContent:      deps.AuditExportContent,
		retention:               catalog.Retention(),
		classes:                 catalog.Class(),
		academicUnits:           deps.AcademicUnits,
		authorizer:              deps.Authorizer,
		people:                  people.New(catalog, deps.Authorizer),
		auditor:                 deps.Auditor.MutationAudit(),
		publisher:               deps.Publisher,
		mail:                    deps.Mail,
		capabilities:            deps.Capabilities,
		plugins:                 deps.Plugins,
		canonicalOrigin:         deps.CanonicalOrigin,
		supportAddress:          supportAddress,
		passwordMinimumLength:   deps.PasswordMinimumLength,
		recentAuthenticationTTL: deps.RecentAuthenticationTTL,
		sessionLimits:           sessionLengthLimits(deps.Sessions, deps.RecentAuthenticationTTL),
		now:                     deps.Now,
	}, nil
}
