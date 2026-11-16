// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package access

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// mutationAttemptAudits is the audit store behind a real
// MutationAuditAdapter. It logs each save as "begin" and each completion as
// "fail" or "success" into the test's shared event log.
type mutationAttemptAudits struct {
	store.AuditStore
	events      *[]string
	saveErr     error
	completeErr error
	saved       []*model.AuditEvent
	completions []*store.AuditCompletion
}

func (a *mutationAttemptAudits) Save(_ context.Context, event *model.AuditEvent) (*model.AuditEvent, error) {
	*a.events = append(*a.events, "begin")
	if a.saveErr != nil {
		return nil, a.saveErr
	}
	saved := *event
	saved.ID = model.NewAuditEventID()
	a.saved = append(a.saved, &saved)
	return &saved, nil
}

func (a *mutationAttemptAudits) Complete(_ context.Context, completion *store.AuditCompletion) (*model.AuditEvent, error) {
	*a.events = append(*a.events, string(completion.Status))
	a.completions = append(a.completions, completion)
	if a.completeErr != nil {
		return nil, a.completeErr
	}
	return &model.AuditEvent{ID: completion.ID, Status: completion.Status, ErrorCode: completion.ErrorCode}, nil
}

// attempt is the one recorded attempt.
func (a *mutationAttemptAudits) attempt(t *testing.T) *model.AuditEvent {
	t.Helper()
	require.Len(t, a.saved, 1, "recorded attempts")
	return a.saved[0]
}

// completion is the one recorded completion.
func (a *mutationAttemptAudits) completion(t *testing.T) *store.AuditCompletion {
	t.Helper()
	require.Len(t, a.completions, 1, "recorded completions")
	return a.completions[0]
}

func mutationAttemptAuditor(audits *mutationAttemptAudits) MutationAuditAdapter {
	return MutationAuditAdapter{audit: &Auditor{deps: AuditorDependencies{Audits: audits, NodeID: "test-node"}, now: time.Now}}
}

func mutationAttemptInvocation() invocation.Invocation {
	return invocation.New(model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password",
		AuthenticationStrength: model.AuthenticationSingleFactor, AuthenticatedAt: model.NowUTC(), ClientType: model.SessionClientWeb,
	}, model.RequestMetadata{RequestID: "mutation-attempt"})
}

func TestMutationAttemptBeginFailurePreventsMutation(t *testing.T) {
	t.Parallel()

	events := []string{}
	saveErr := errors.New("audit store unavailable")
	audits := &mutationAttemptAudits{events: &events, saveErr: saveErr}
	mutated := false
	_, err := RunAudited(
		context.Background(), mutationAttemptAuditor(audits),
		Mutation{
			Invocation: mutationAttemptInvocation(),
			Action:     model.ActionAcademicUnitManage,
			Resource:   model.Resource{Type: model.ResourceAcademicUnit, ID: model.NewID()},
			Operation:  "create",
			At:         time.Now(),
		},
		func(context.Context, MutationReference) (string, error) {
			mutated = true
			return "", nil
		},
		func(err error) error { return apperror.New("programme.conflict").Wrap(err) },
	)
	require.True(t, apperror.Is(err, "audit.unavailable"), "RunAudited() error = %v, want audit.unavailable", err)
	require.ErrorIs(t, err, saveErr, "RunAudited() error")
	require.False(t, mutated, "named mutation ran after begin failure")
	require.Equal(t, []string{"begin"}, events)
}

func TestMutationAttemptRejectsMutationWithoutTime(t *testing.T) {
	t.Parallel()

	events := []string{}
	audits := &mutationAttemptAudits{events: &events}
	mutated := false
	_, err := RunAudited(
		context.Background(), mutationAttemptAuditor(audits),
		Mutation{
			Invocation: mutationAttemptInvocation(),
			Action:     model.ActionAcademicUnitManage,
			Resource:   model.Resource{Type: model.ResourceAcademicUnit, ID: model.NewID()},
			Operation:  "create",
		},
		func(context.Context, MutationReference) (string, error) {
			mutated = true
			return "", nil
		},
		func(err error) error { return err },
	)
	var appErr *apperror.Error
	require.ErrorAs(t, err, &appErr, "RunAudited() error")
	require.Equal(t, "audit.internal", appErr.Code())
	require.False(t, mutated, "named mutation ran without a mutation time")
	require.Empty(t, events, "an audit was begun for a mutation without a time")
}

func TestMutationAttemptUsesIndependentAuditScope(t *testing.T) {
	t.Parallel()

	events := []string{}
	scopeID := model.NewID()
	resource := model.Resource{Type: model.ResourceAcademicPeriod, ID: model.NewID()}
	audits := &mutationAttemptAudits{events: &events}
	got, err := RunAudited(
		context.Background(), mutationAttemptAuditor(audits),
		Mutation{
			Invocation: mutationAttemptInvocation(),
			Action:     model.ActionAcademicPeriodManage, Resource: resource,
			ScopeType: model.RoleScopeAcademicUnit, ScopeID: scopeID,
			Operation: "create",
			At:        time.Now(),
		},
		func(_ context.Context, reference MutationReference) (model.AuditEventID, error) {
			events = append(events, "mutate")
			return reference.ID, nil
		},
		func(error) error { return nil },
	)
	require.NoError(t, err, "RunAudited()")
	attempt := audits.attempt(t)
	require.Equal(t, attempt.ID, got, "RunAudited()")
	require.Equal(t, resource, attempt.Resource)
	require.Equal(t, model.RoleScopeAcademicUnit, attempt.ScopeType)
	require.Equal(t, scopeID, attempt.ScopeID)
	// Contract (guardrail 8): the audit begins before the durable write.
	require.Equal(t, []string{"begin", "mutate"}, events)
}

func TestMutationAttemptMapsStoreFailureBeforeCompletingAttempt(t *testing.T) {
	t.Parallel()

	events := []string{}
	storeErr := errors.New("store failed")
	mapped := apperror.New("programme.conflict").Wrap(storeErr)
	audits := &mutationAttemptAudits{events: &events}
	at := time.UnixMilli(500)
	_, err := RunAudited(
		context.Background(), mutationAttemptAuditor(audits),
		Mutation{
			Invocation: mutationAttemptInvocation(),
			Action:     model.ActionAcademicUnitManage,
			Resource:   model.Resource{Type: model.ResourceAcademicUnit, ID: model.NewID()},
			Operation:  "patch",
			At:         at,
		},
		func(_ context.Context, reference MutationReference) (string, error) {
			events = append(events, "mutate")
			require.Equal(t, audits.attempt(t).ID, reference.ID)
			require.True(t, model.TimeUTC(at).Equal(reference.MutationAt))
			return "", storeErr
		},
		func(err error) error {
			events = append(events, "map")
			require.Equal(t, storeErr, err)
			return mapped
		},
	)
	require.Equal(t, err, mapped, "RunAudited() error = %v, want mapped error", err)
	completion := audits.completion(t)
	require.Equal(t, audits.attempt(t).ID, completion.ID, "failure completion = id")
	require.Equal(t, model.AuditStatusFail, completion.Status, "failure completion status")
	require.Equal(t, mapped.Code(), completion.ErrorCode, "failure completion code")
	// Contract (guardrail 8): the audit begins before the durable write, and the
	// audit outcome follows the write.
	require.Equal(t, []string{"begin", "mutate", "map", "fail"}, events)
}

func TestMutationAttemptFailureCompletionErrorJoinsOriginalFailure(t *testing.T) {
	t.Parallel()

	events := []string{}
	storeErr := errors.New("store failed")
	completeErr := errors.New("audit store unavailable")
	audits := &mutationAttemptAudits{events: &events, completeErr: completeErr}
	_, err := RunAudited(
		context.Background(), mutationAttemptAuditor(audits),
		Mutation{
			Invocation: mutationAttemptInvocation(),
			Action:     model.ActionAcademicUnitManage,
			Resource:   model.Resource{Type: model.ResourceAcademicUnit, ID: model.NewID()},
			Operation:  "archive",
			At:         time.Now(),
		},
		func(context.Context, MutationReference) (struct{}, error) {
			return struct{}{}, storeErr
		},
		func(err error) error { return apperror.New("programme.conflict").Wrap(err) },
	)
	// The audit failure must stay observable without replacing the original
	// classification that callers and transport mapping resolve first.
	appErr, ok := apperror.As(err)
	require.True(t, ok, "RunAudited() error")
	require.Equal(t, "programme.conflict", appErr.Code(), "RunAudited() error")
	require.ErrorIs(t, err, storeErr, "RunAudited() error")
	require.ErrorIs(t, err, completeErr, "RunAudited() error")
}

func TestMutationAttemptRejectsInvalidMappedError(t *testing.T) {
	t.Parallel()

	for name, mapped := range map[string]error{
		"nil":   nil,
		"plain": errors.New("unmapped store failure"),
	} {
		t.Run(name, func(t *testing.T) {
			events := []string{}
			audits := &mutationAttemptAudits{events: &events}
			_, err := RunAudited(
				context.Background(), mutationAttemptAuditor(audits),
				Mutation{
					Invocation: mutationAttemptInvocation(),
					Action:     model.ActionAcademicUnitManage,
					Resource:   model.Resource{Type: model.ResourceAcademicUnit, ID: model.NewID()},
					Operation:  "create",
					At:         time.Now(),
				},
				func(context.Context, MutationReference) (struct{}, error) {
					return struct{}{}, errors.New("store failed")
				},
				func(error) error { return mapped },
			)
			appErr, ok := apperror.As(err)
			require.True(t, ok)
			require.Equal(t, "audit.internal", appErr.Code())
			require.Equal(t, "audit.internal", audits.completion(t).ErrorCode)
		})
	}
}

func TestMutationAttemptPreservesMappedErrorWrapper(t *testing.T) {
	t.Parallel()

	events := []string{}
	audits := &mutationAttemptAudits{events: &events}
	appFailure := apperror.New("role.conflict")
	mapped := fmt.Errorf("persistence context: %w", appFailure)
	_, err := RunAudited(
		context.Background(), mutationAttemptAuditor(audits),
		Mutation{
			Invocation: mutationAttemptInvocation(),
			Action:     model.ActionRoleManage,
			Resource:   model.Resource{Type: model.ResourceInstitution, ID: model.NewID()},
			Operation:  "patch",
			At:         time.Now(),
		},
		func(context.Context, MutationReference) (struct{}, error) {
			return struct{}{}, errors.New("store failed")
		},
		func(error) error { return mapped },
	)
	require.Equal(t, mapped, err, "RunAudited() error")
	require.Equal(t, appFailure.Code(), audits.completion(t).ErrorCode)
}

func TestMutationAttemptReturnsSuccessWithoutCompletingIt(t *testing.T) {
	t.Parallel()

	events := []string{}
	resource := model.Resource{Type: model.ResourceAcademicUnit, ID: model.NewID()}
	value := map[string]any{"name": "computer-science"}
	prior := map[string]any{"name": "computing"}
	inv := mutationAttemptInvocation()
	audits := &mutationAttemptAudits{events: &events}
	// A local, sub-microsecond instant far from the wall clock: only the given
	// At, normalised, can reach the mutation reference.
	at := time.UnixMilli(500).Add(789 * time.Nanosecond).In(time.FixedZone("WAT", 3600))
	got, err := RunAudited(
		context.Background(), mutationAttemptAuditor(audits),
		Mutation{
			Invocation: inv,
			Action:     model.ActionAcademicUnitManage,
			Resource:   resource,
			Operation:  "patch",
			Value:      value,
			Prior:      prior,
			At:         at,
		},
		func(_ context.Context, reference MutationReference) (string, error) {
			events = append(events, "mutate")
			require.Equal(t, audits.attempt(t).ID, reference.ID)
			require.Equal(t, time.UnixMilli(500).UTC(), reference.MutationAt)
			return "saved", nil
		},
		func(error) error {
			require.FailNow(t, "error mapper called on success")
			return nil
		},
	)
	require.NoError(t, err, "RunAudited()")
	require.Equal(t, "saved", got, "RunAudited()")
	attempt := audits.attempt(t)
	require.Equal(t, inv.Principal().UserID, attempt.ActorID)
	require.Equal(t, inv.RequestMetadata().RequestID, attempt.RequestID)
	require.Equal(t, string(model.ActionAcademicUnitManage), attempt.Action)
	require.Equal(t, resource, attempt.Resource)
	require.Equal(t, model.AuditStatusAttempt, attempt.Status)
	require.JSONEq(t, `{"operation":"patch","value":{"name":"computer-science"}}`, string(attempt.Parameters))
	require.JSONEq(t, `{"name":"computing"}`, string(attempt.PriorState))
	require.Empty(t, audits.completions, "a successful attempt is completed by the Store mutation")
	// RunAudited reads no clock: no parameter can supply one.
	runner := reflect.TypeOf(RunAudited[string])
	for i := range runner.NumIn() {
		require.NotEqual(t, reflect.TypeFor[func() time.Time](), runner.In(i), "RunAudited parameter %d", i)
	}
	// Contract (guardrail 8): the audit begins before the durable write.
	require.Equal(t, []string{"begin", "mutate"}, events)
}
