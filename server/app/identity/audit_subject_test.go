// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/access"
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
	service, err := access.NewAuditor(access.AuditorDependencies{Audits: audits, Institutions: subjectAuditInstitutions{institution: singleton}, NodeID: "node-subject"})
	require.NoError(t, err)
	caller := selfAuditSessionInvocation()
	me := caller.Principal().UserID
	institution := model.Resource{Type: model.ResourceInstitution, ID: singleton.ID.String()}

	mfa := &Service{securityAudit: service}
	_, err = mfa.beginMFAAudit(context.Background(), caller, model.ActionMFAActivate, institution, nil)
	require.NoError(t, err)
	require.Equal(t, me, audits.saved.SubjectUserID, "self MFA audit is not in the caller's history")

	target := model.NewUserID()
	_, err = mfa.beginMFAAudit(context.Background(), caller, model.ActionUserMFAReset,
		model.Resource{Type: model.ResourceUser, ID: target.String()}, nil)
	require.NoError(t, err)
	require.Equal(t, target, audits.saved.SubjectUserID, "an administrator reset names its target")
	require.Equal(t, me, audits.saved.ActorID)

	_, err = (service.MutationAudit()).Begin(context.Background(), caller, model.ActionSessionManage,
		model.Resource{Type: model.ResourceUser, ID: me.String()}, "revoke_own_sessions", nil, nil)
	require.NoError(t, err)
	require.Equal(t, me, audits.saved.SubjectUserID, "a User resource is its own subject")

	member, classID := model.NewUserID(), model.NewClassID()
	_, err = access.RunAudited(context.Background(), service.MutationAudit(), access.Mutation{
		Invocation: caller, Action: model.ActionClassMembersManage,
		Resource: model.Resource{Type: model.ResourceClass, ID: classID.String()}, Operation: "enroll", Subject: member,
	}, time.Now, func(context.Context, access.MutationReference) (struct{}, error) { return struct{}{}, nil },
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
