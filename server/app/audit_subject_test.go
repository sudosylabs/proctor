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

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

func selfAuditSessionInvocation() invocation.Invocation {
	at := time.Now().UTC()
	return invocation.New(model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password",
		AuthenticationStrength: model.AuthenticationSingleFactor, AuthenticatedAt: at, ClientType: model.SessionClientWeb,
	}, model.RequestMetadata{RequestID: "self-audit"})
}

func TestAuditedMutationWithSubjectRefusesAnAuditorThatWouldDropIt(t *testing.T) {
	t.Parallel()
	events := []string{}
	_, err := access.RunAudited(context.Background(), &academicUnitCommandAuditor{events: &events}, access.Mutation{
		Invocation: selfAuditSessionInvocation(), Action: model.ActionRoleBindingManage,
		Resource: model.Resource{Type: model.ResourceInstitution, ID: model.NewID()}, Operation: "create_binding",
		Subject: model.NewUserID(),
	}, time.Now, func(context.Context, access.MutationReference) (struct{}, error) {
		require.Fail(t, "the mutation ran without a durable subject")
		return struct{}{}, nil
	}, func(err error) error { return err })
	require.True(t, apperror.Is(err, "audit.internal"), "error = %v", err)
	require.Empty(t, events)

	// Begin takes the whole Mutation, so the production auditor is always
	// handed the subject: it is durable before the mutation runs, and a
	// subject that cannot be recorded stops the mutation before any write.
	audits := &systemAuditStoreFake{}
	service, err := access.NewAuditor(access.AuditorDependencies{Audits: audits, Institutions: securityInstitutionStore{}, NodeID: "node-subject"})
	require.NoError(t, err)
	subject := model.NewUserID()
	attempt := access.Mutation{
		Invocation: selfAuditSessionInvocation(), Action: model.ActionRoleBindingManage,
		Resource: model.Resource{Type: model.ResourceInstitution, ID: model.NewID()}, Operation: "create_binding",
		Subject: subject,
	}
	ran := false
	_, err = access.RunAudited(context.Background(), service.MutationAudit(), attempt, time.Now,
		func(context.Context, access.MutationReference) (struct{}, error) {
			ran = true
			require.NotNil(t, audits.saved, "the mutation ran before its audit attempt")
			require.Equal(t, subject, audits.saved.SubjectUserID, "the mutation ran without a durable subject")
			return struct{}{}, nil
		}, func(err error) error { return err })
	require.NoError(t, err)
	require.True(t, ran)

	audits.saved = nil
	attempt.Subject = "not-a-user"
	_, err = access.RunAudited(context.Background(), service.MutationAudit(), attempt, time.Now,
		func(context.Context, access.MutationReference) (struct{}, error) {
			require.Fail(t, "the mutation ran with a subject that cannot be recorded")
			return struct{}{}, nil
		}, func(err error) error { return err })
	require.True(t, apperror.Is(err, "audit.internal"), "error = %v", err)
	require.Nil(t, audits.saved)
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
