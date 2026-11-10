// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/academic"
	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/exam/correction"
	"github.com/sudosylabs/proctor/server/app/exam/resource"
	"github.com/sudosylabs/proctor/server/app/exam/review"
	"github.com/sudosylabs/proctor/server/app/exam/sitting"
	"github.com/sudosylabs/proctor/server/app/exam/workspace"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/onboarding"

	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/app/jobhandler"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/user"
)

type examinationConstruction struct {
	authoring              *exam.Authoring
	revisions              *exam.Publication
	sittings               *sitting.Service
	sittingMail            *mail.SittingComposer
	sittingMailPreparation sittingScheduleMailPreparationAdapter
	attempts               *attempt.Service
	reviews                *review.Service
	records                *exam.Records
	exports                *exam.Exports
	resources              *resource.Service
	corrections            *correction.Service
	starterWorkspace       *workspace.Service
}

// applicationFoundation holds the shared mechanics constructed before any
// focused application service. It is a private construction result, not a
// runtime service locator.
type applicationFoundation struct {
	hasher      *identity.PasswordHasher
	attempts    *identity.AttemptAccounting
	invalidator *identity.CacheInvalidator
	realtime    *realtimeService
	audit       *access.Auditor
	mail        *mail.Composer
	mailHealth  *MailHealth
}

type accountConstruction struct {
	identity *identity.Service
	// invitationAcceptor reaches the onboarding area's Invitation
	// acceptance once constructOnboarding builds it.
	invitationAcceptor *externalInvitationAcceptor
	onboarding         *onboarding.Service
	onboardingPorts    onboarding.Ports
	// wake reaches the Job runtime once constructJobs builds it.
	wake *jobWake
}

type accessAcademicConstruction struct {
	authorization  *access.Authorizer
	capabilities   identity.AccessPolicyCapabilitySource
	administration *administration.Service
	academic       *academic.Service
}

type profileFileConstruction struct {
	users *user.Service
	// defaultJobs proposes default profile picture generation; it exists only
	// when Jobs are enabled and is woken by the Job runtime once built.
	defaultJobs *jobhandler.DefaultProfilePictureJobs
}

type jobConstruction struct {
	runtime    *job.Engine
	operations *jobOperationsService
	mail       *mailService
}

type administrationConstruction struct {
	// search spans Exams, Users and Classes, so it is built once every one of
	// those services exists.
	search *searchService
	// counts summarises Invitations, Jobs, mail, and onboarding imports, so
	// it is built once every one of their services exists.
	counts *administrationCountsService
}

func validateApplicationDependencies(deps Dependencies) error {
	if deps.Store == nil {
		return errors.New("store is required")
	}
	if deps.Cache == nil {
		return errors.New("cache is required")
	}
	if deps.MailDeliverySender == nil {
		return errors.New("mail delivery sender is required")
	}
	if deps.MailTemplateRenderer == nil {
		return errors.New("mail template renderer is required")
	}
	if deps.Registry == nil {
		return errors.New("external provider registry is required")
	}
	if deps.FileContent == nil {
		return errors.New("file content is required")
	}
	if deps.NodeID == "" {
		return errors.New("node ID is required")
	}
	if deps.AuthenticationDiagnostics == nil {
		return errors.New("authentication diagnostics is required")
	}
	if deps.RealtimeDiagnostics == nil {
		return errors.New("realtime diagnostics is required")
	}
	if deps.RecoveryDiagnostics == nil {
		return errors.New("recovery diagnostics is required")
	}
	return nil
}

func constructApplicationFoundation(deps Dependencies) (applicationFoundation, error) {
	hasher, err := identity.NewPasswordHasher(identity.PasswordHasherDependencies{Settings: deps.Password, Recorder: deps.PasswordWorkRecorder})
	if err != nil {
		return applicationFoundation{}, err
	}
	attempts, err := identity.NewAttemptAccounting(identity.AttemptAccountingDependencies{Cache: deps.Cache})
	if err != nil {
		return applicationFoundation{}, err
	}
	invalidator, err := identity.NewCacheInvalidator(identity.CacheInvalidatorDependencies{Cache: deps.Cache, Diagnostics: deps.AuthenticationDiagnostics})
	if err != nil {
		return applicationFoundation{}, err
	}
	realtimeDelivery, err := realtime.New(realtime.Dependencies{AuthenticationInvalidator: invalidator, Diagnostics: deps.RealtimeDiagnostics})
	if err != nil {
		return applicationFoundation{}, err
	}
	realtime, err := newRealtimeServiceWithDelivery(realtimeServiceDependencies{Delivery: realtimeDelivery, Diagnostics: deps.RealtimeDiagnostics})
	if err != nil {
		return applicationFoundation{}, err
	}
	realtime.attempts = deps.Store.ExamAttempt()
	audit, err := access.NewAuditor(access.AuditorDependencies{Audits: deps.Store.Audit(), Institutions: deps.Store.Institution(), NodeID: deps.NodeID})
	if err != nil {
		return applicationFoundation{}, err
	}
	institutionName := (&institutionNameCache{institutions: deps.Store.Institution(), now: time.Now})
	mail, err := newDirectMailPreparer(deps.MailTemplateRenderer, deps.MailDeliverySender, deps.MailSecretSealer, institutionName.Name)
	if err != nil {
		return applicationFoundation{}, err
	}
	return applicationFoundation{
		hasher:      hasher,
		attempts:    attempts,
		invalidator: invalidator,
		realtime:    realtime,
		audit:       audit,
		mail:        mail,
		mailHealth:  newMailHealth(deps.MailDeliverySender.Enabled()),
	}, nil
}

func assembleApplication(
	deps Dependencies,
	foundation applicationFoundation,
	accounts accountConstruction,
	access accessAcademicConstruction,
	examinations examinationConstruction,
	profiles profileFileConstruction,
	jobs jobConstruction,
	administration administrationConstruction,
) *App {
	return &App{
		Identity:                    accounts.identity,
		Onboarding:                  accounts.onboarding,
		Administration:              access.administration,
		Academic:                    access.academic,
		ExamAuthoring:               examinations.authoring,
		ExamPublication:             examinations.revisions,
		Sittings:                    examinations.sittings,
		Attempts:                    examinations.attempts,
		Reviews:                     examinations.reviews,
		ExamRecords:                 examinations.records,
		ExamExports:                 examinations.exports,
		Resources:                   examinations.resources,
		Corrections:                 examinations.corrections,
		StarterWorkspaces:           examinations.starterWorkspace,
		Users:                       profiles.users,
		realtimeService:             foundation.realtime,
		searchService:               administration.search,
		administrationCountsService: administration.counts,
		realtimeAdmissionService: &realtimeAdmissionService{
			principals: accounts.identity, exams: examinations.authoring, sittings: examinations.sittings,
			authorization: access.authorization, operational: deps.OperationalRecorder,
		},
		jobs:                 jobs.runtime,
		jobOperationsService: jobs.operations,
		mailService:          jobs.mail,
	}
}
