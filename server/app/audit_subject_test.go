// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func selfAuditSessionInvocation() invocation.Invocation {
	at := time.Now().UTC()
	return invocation.New(model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password",
		AuthenticationStrength: model.AuthenticationSingleFactor, AuthenticatedAt: at, ClientType: model.SessionClientWeb,
	}, model.RequestMetadata{RequestID: "self-audit"})
}

func TestAuditWritersNameTheSubjectUser(t *testing.T) {
	t.Parallel()
	audits := &systemAuditStoreFake{}
	singleton := &model.Institution{ID: model.NewInstitutionID()}
	service, err := newAuditService(auditServiceDependencies{Audits: audits, Institutions: subjectAuditInstitutions{institution: singleton}, NodeID: "node-subject"})
	require.NoError(t, err)
	caller := selfAuditSessionInvocation()
	me := caller.Principal().UserID
	institution := model.Resource{Type: model.ResourceInstitution, ID: singleton.ID.String()}

	_, err = (mfaAuditAdapter{audit: service}).Begin(context.Background(), caller, model.ActionMFAActivate, institution, nil)
	require.NoError(t, err)
	require.Equal(t, me, audits.saved.SubjectUserID, "self MFA audit is not in the caller's history")

	target := model.NewUserID()
	_, err = (mfaAuditAdapter{audit: service}).Begin(context.Background(), caller, model.ActionUserMFAReset,
		model.Resource{Type: model.ResourceUser, ID: target.String()}, nil)
	require.NoError(t, err)
	require.Equal(t, target, audits.saved.SubjectUserID, "an administrator reset names its target")
	require.Equal(t, me, audits.saved.ActorID)

	_, err = (mutationAuditAdapter{audit: service}).Begin(context.Background(), caller, model.ActionSessionManage,
		model.Resource{Type: model.ResourceUser, ID: me.String()}, "revoke_own_sessions", nil, nil)
	require.NoError(t, err)
	require.Equal(t, me, audits.saved.SubjectUserID, "a User resource is its own subject")

	member, classID := model.NewUserID(), model.NewClassID()
	_, err = runAuditedMutation(context.Background(), mutationAuditAdapter{audit: service}, mutationAttempt{
		Invocation: caller, Action: model.ActionClassMembersManage,
		Resource: model.Resource{Type: model.ResourceClass, ID: classID.String()}, Operation: "enroll", Subject: member,
	}, time.Now, func(context.Context, mutationAttemptReference) (struct{}, error) { return struct{}{}, nil },
		func(err error) error { return err })
	require.NoError(t, err)
	require.Equal(t, member, audits.saved.SubjectUserID)
	require.Equal(t, model.RoleScopeClass, audits.saved.ScopeType, "the subject path keeps the resource scope")
	require.Equal(t, classID.String(), audits.saved.ScopeID)
}

type subjectAuditInstitutions struct {
	store.InstitutionStore
	institution *model.Institution
}

func (s subjectAuditInstitutions) GetSingleton(context.Context) (*model.Institution, error) {
	return s.institution, nil
}

func TestAuditedMutationWithSubjectRefusesAnAuditorThatWouldDropIt(t *testing.T) {
	t.Parallel()
	events := []string{}
	_, err := runAuditedMutation(context.Background(), &academicUnitCommandAuditor{events: &events}, mutationAttempt{
		Invocation: selfAuditSessionInvocation(), Action: model.ActionRoleBindingManage,
		Resource: model.Resource{Type: model.ResourceInstitution, ID: model.NewID()}, Operation: "create_binding",
		Subject: model.NewUserID(),
	}, time.Now, func(context.Context, mutationAttemptReference) (struct{}, error) {
		require.Fail(t, "the mutation ran without a durable subject")
		return struct{}{}, nil
	}, func(err error) error { return err })
	require.True(t, apperror.Is(err, "audit.internal"), "error = %v", err)
	require.Empty(t, events)
}

func TestAuditEventUserResourceIsAlwaysItsSubject(t *testing.T) {
	t.Parallel()
	user := model.NewUserID()
	event := &model.AuditEvent{
		Action: string(model.ActionUserManage), Resource: model.Resource{Type: model.ResourceUser, ID: user.String()},
		ScopeType: model.RoleScopeInstitution, ScopeID: model.NewID(), Status: model.AuditStatusSuccess, NodeID: "node",
	}
	event.PrepareCreate(model.NewAuditEventID(), time.Now())
	require.Equal(t, user, event.SubjectUserID)
	require.NoError(t, event.Validate())

	event.SubjectUserID = ""
	require.Error(t, event.Validate(), "a User resource without its subject must not persist")
	event.SubjectUserID = model.NewUserID()
	require.Error(t, event.Validate(), "a User resource with another subject must not persist")
}
