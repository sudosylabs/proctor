// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"time"

	"github.com/sudosylabs/proctor/server/app/academic"
	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/model"
)

func constructAccessAndAcademics(
	deps Dependencies,
	foundation applicationFoundation,
) (accessAcademicConstruction, error) {
	scopeResolver, err := access.NewScopeResolver(access.ScopeResolverDependencies{Institutions: deps.Store.Institution(), AcademicUnits: deps.Store.AcademicUnit(), Classes: deps.Store.Class(), Users: deps.Store.User(), ClassMembers: deps.Store.ClassMember(), Exams: deps.Store.ExamAuthoring(), Sittings: deps.Store.ExamSitting(), Submissions: deps.Store.ExamSubmission(), AcademicPeriods: deps.Store.AcademicPeriod(), Programmes: deps.Store.Programme(), ProgrammeLevels: deps.Store.ProgrammeLevel()})
	if err != nil {
		return accessAcademicConstruction{}, err
	}
	authorization, err := access.NewAuthorizer(access.AuthorizerDependencies{Roles: deps.Store.Role(), Bindings: deps.Store.RoleBinding(), Resolver: scopeResolver, Audit: foundation.audit})
	if err != nil {
		return accessAcademicConstruction{}, err
	}
	capabilities := deploymentAccessPolicyCapabilities{
		providers: deps.Registry, mail: deps.MailDeliverySender, health: foundation.mailHealth,
		mfaEnabled: deps.MFA.Enabled,
	}
	administrations, err := administration.New(administration.Dependencies{
		Store: deps.Store, Authorizer: authorization, Auditor: foundation.audit.MutationAudit(), Capabilities: capabilities,
		Mail: foundation.mail, Effects: administrationRealtimeEffects{realtime: foundation.realtime},
		CanonicalOrigin: deps.PublicURL, SupportAddress: deps.SupportAddress, PasswordMinimumLength: deps.Password.MinimumLength,
		RecentAuthenticationTTL: deps.RecentAuthenticationTTL, Sessions: deps.Sessions, Now: time.Now,
	})
	if err != nil {
		return accessAcademicConstruction{}, err
	}
	academics, err := academic.New(academic.Dependencies{
		Store: deps.Store, Authorizer: authorization, Auditor: foundation.audit.MutationAudit(), Mail: foundation.mail,
		Effects: academicRealtimeEffects{sittings: deps.Store.ExamSitting(), realtime: foundation.realtime},
		Now:     time.Now, NewID: model.NewID,
	})
	if err != nil {
		return accessAcademicConstruction{}, err
	}
	return accessAcademicConstruction{
		authorization: authorization, capabilities: capabilities, administration: administrations,
		academic: academics,
	}, nil
}
