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

// orderedAuditStoreFake is the audit store the production mutation auditor
// writes to. It records every Save it is asked for, accepted or not, on the
// same log as the mutation the attempt guards, so an empty log means the store
// was never reached.
type orderedAuditStoreFake struct {
	systemAuditStoreFake
	events []string
}

func (f *orderedAuditStoreFake) Save(ctx context.Context, event *model.AuditEvent) (*model.AuditEvent, error) {
	f.events = append(f.events, "audit-save")

	return f.systemAuditStoreFake.Save(ctx, event)
}

// TestAuditedMutationPersistsItsSubjectAndRefusesMalformedSubjectOrScope runs
// access.RunAudited over the production access.MutationAuditAdapter and a
// recording audit store, so every assertion is about what production code
// writes and refuses.
func TestAuditedMutationPersistsItsSubjectAndRefusesMalformedSubjectOrScope(t *testing.T) {
	t.Parallel()
	newAuditor := func(t *testing.T) (*orderedAuditStoreFake, access.MutationAuditAdapter) {
		t.Helper()
		audits := &orderedAuditStoreFake{}
		service, err := access.NewAuditor(access.AuditorDependencies{Audits: audits, Institutions: securityInstitutionStore{}, NodeID: "node-subject"})
		require.NoError(t, err)

		return audits, service.MutationAudit()
	}
	bindingMutation := func() access.Mutation {
		return access.Mutation{
			Invocation: selfAuditSessionInvocation(),
			Action:     model.ActionRoleBindingManage,
			Resource:   model.Resource{Type: model.ResourceInstitution, ID: model.NewID()},
			Operation:  "create_binding",
			Subject:    model.NewUserID(),
			At:         time.Now(),
		}
	}
	unmapped := func(err error) error { return err }

	recorded := []struct {
		name  string
		scope func(*access.Mutation)
	}{
		{"at the resource's own scope", func(*access.Mutation) {}},
		{"at an explicit scope", func(m *access.Mutation) { m.ScopeType, m.ScopeID = model.RoleScopeInstitution, m.Resource.ID }},
	}
	for _, test := range recorded {
		t.Run("persists the subject before the mutation runs "+test.name, func(t *testing.T) {
			t.Parallel()
			audits, auditor := newAuditor(t)
			attempt := bindingMutation()
			test.scope(&attempt)

			var durable *model.AuditEvent
			var reference access.MutationReference
			_, err := access.RunAudited(context.Background(), auditor, attempt,
				func(_ context.Context, given access.MutationReference) (struct{}, error) {
					audits.events = append(audits.events, "mutate")
					durable, reference = audits.saved, given
					return struct{}{}, nil
				}, unmapped)
			require.NoError(t, err)
			// Ordering contract: the audit attempt is saved before the
			// mutation it guards runs, and nothing else is written.
			require.Equal(t, []string{"audit-save", "mutate"}, audits.events)
			require.NotNil(t, durable, "the mutation ran before its audit attempt was saved")
			require.Equal(t, attempt.Subject, durable.SubjectUserID)
			require.Equal(t, model.AuditStatusAttempt, durable.Status)
			require.Equal(t, model.RoleScopeInstitution, durable.ScopeType)
			require.Equal(t, attempt.Resource.ID, durable.ScopeID)
			require.Equal(t, durable.ID, reference.ID, "the mutation was not handed the saved attempt")
		})
	}

	refused := []struct {
		name    string
		malform func(*access.Mutation)
	}{
		{"a malformed subject", func(m *access.Mutation) { m.Subject = "not-a-user" }},
		{"an unknown explicit scope type", func(m *access.Mutation) { m.ScopeType, m.ScopeID = "galaxy", m.Resource.ID }},
		{"a malformed explicit scope ID", func(m *access.Mutation) { m.ScopeType, m.ScopeID = model.RoleScopeInstitution, "not an id" }},
		{"an explicit scope type without its ID", func(m *access.Mutation) { m.ScopeType = model.RoleScopeInstitution }},
	}
	for _, test := range refused {
		t.Run("refuses "+test.name+" before saving or mutating", func(t *testing.T) {
			t.Parallel()
			audits, auditor := newAuditor(t)
			attempt := bindingMutation()
			test.malform(&attempt)

			_, err := access.RunAudited(context.Background(), auditor, attempt,
				func(context.Context, access.MutationReference) (struct{}, error) {
					audits.events = append(audits.events, "mutate")
					return struct{}{}, nil
				}, unmapped)
			require.True(t, apperror.Is(err, "audit.internal"), "error = %v", err)
			// Ordering contract: a refused Mutation is stopped before the
			// audit store is asked to save and before the mutation runs.
			require.Empty(t, audits.events)
			require.Nil(t, audits.saved)
		})
	}
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
