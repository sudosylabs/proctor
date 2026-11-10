// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package user

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type userSettingsStoreFake struct {
	store.UserSettingsStore
	document       *model.UserSettingsDocument
	err            error
	got            model.UserID
	reads          int
	replaceInput   *store.UserSettingsReplacement
	replaceCommand *store.CommandIdempotency
	replaceResult  *store.UserSettingsReplacementResult
	replaceErr     error
	events         *[]string
}

func (s *userSettingsStoreFake) Get(_ context.Context, userID model.UserID) (*model.UserSettingsDocument, error) {
	s.got = userID
	s.reads++
	if s.err != nil {
		return nil, s.err
	}
	return s.document.Clone(), nil
}

func (s *userSettingsStoreFake) Replace(
	_ context.Context,
	input *store.UserSettingsReplacement,
	command *store.CommandIdempotency,
) (*store.UserSettingsReplacementResult, error) {
	if s.events != nil {
		*s.events = append(*s.events, "store")
	}
	s.replaceInput = input
	s.replaceCommand = command
	if s.replaceErr != nil {
		return nil, s.replaceErr
	}
	return s.replaceResult, nil
}

func TestUserSettingsReadOwnPreservesExactSource(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	principal := userSettingsSessionPrincipal(at)
	source := "{\n  // keep this comment\n  \"editor.fontFamily\": \"A\\\\B\",\n  \"future.unknown\": true,\n}\n"
	persistence := &userSettingsStoreFake{document: &model.UserSettingsDocument{
		UserID: principal.UserID, Source: source,
		FormatVersion: model.UserSettingsFormatVersion1,
		Revision:      model.NewUserSettingsRevision(),
		CreatedAt:     at.Add(-time.Hour), UpdatedAt: at,
	}}
	service := newSettingsServiceForTest(t, persistence, &settingsAuditFake{}, time.Now)

	result, err := service.ReadOwnUserSettings(context.Background(), invocation.New(principal, model.RequestMetadata{}))
	require.NoError(t, err)
	require.Equal(t, principal.UserID, persistence.got, "Get() user")
	require.Equal(t, 1, persistence.reads, "Get() user")
	require.Equal(t, source, result.Source)
	require.Equal(t, model.UserSettingsFormatVersion1, result.FormatVersion)
	require.Equal(t, persistence.document.Revision, result.Revision)
	require.True(t, result.Writable)
	require.True(t, result.UpdatedAt.Equal(at))
}

func TestUserSettingsReadOwnPreservesUnsupportedFormatAsOpaqueReadOnlySource(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	principal := userSettingsSessionPrincipal(at)
	// This deliberately is not version-1 JSONC. An older server must preserve
	// newer grammar as bounded opaque source instead of attempting to parse it.
	const source = "future-format(source => must.remain.exact);\n"
	persistence := &userSettingsStoreFake{document: &model.UserSettingsDocument{
		UserID: principal.UserID, Source: source, FormatVersion: 2,
		Revision: model.NewUserSettingsRevision(), CreatedAt: at.Add(-time.Hour), UpdatedAt: at,
	}}
	service := newSettingsServiceForTest(t, persistence, &settingsAuditFake{}, time.Now)

	result, err := service.ReadOwnUserSettings(context.Background(), invocation.New(principal, model.RequestMetadata{}))
	require.NoError(t, err)
	require.Equal(t, source, result.Source)
	require.Equal(t, 2, result.FormatVersion)
	require.Equal(t, persistence.document.Revision, result.Revision)
	require.False(t, result.Writable)
	require.True(t, result.UpdatedAt.Equal(at))
	require.Equal(t, 1, persistence.reads, "Get() reads")
}

func TestUserSettingsReadOwnRequiresInteractiveSession(t *testing.T) {
	t.Parallel()
	persistence := &userSettingsStoreFake{}
	service := newSettingsServiceForTest(t, persistence, &settingsAuditFake{}, time.Now)
	pat := model.Principal{
		UserID: model.NewUserID(), CredentialID: model.PrincipalCredentialID(model.NewPersonalAccessTokenID()),
		CredentialType: model.CredentialPersonalAccessToken, AuthenticationMethod: "personal_access_token",
		ClientType: model.SessionClientCLI, CredentialScopes: []string{string(model.ActionUserView)},
	}
	for name, principal := range map[string]model.Principal{
		"personal access token": pat,
		"system invocation":     {},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := service.ReadOwnUserSettings(context.Background(), invocation.New(principal, model.RequestMetadata{}))
			require.True(t, apperror.Is(err, "authentication.token_invalid"), "ReadOwn() error = %v", err)
		})
	}
	require.Equal(t, 0, persistence.reads, "unauthorized calls performed")
}

func TestUserSettingsReadOwnFailsClosedWithoutExposingSource(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	principal := userSettingsSessionPrincipal(at)
	secret := "private.setting.name"
	tests := []struct {
		name  string
		store *userSettingsStoreFake
	}{
		{name: "store failure", store: &userSettingsStoreFake{err: errors.New("database unavailable")}},
		{name: "missing invariant", store: &userSettingsStoreFake{err: store.NewErrNotFound("user_settings_document", principal.UserID.String())}},
		{name: "invalid persisted source", store: &userSettingsStoreFake{document: &model.UserSettingsDocument{
			UserID: principal.UserID, Source: "{\"" + secret + "\": }", FormatVersion: 1,
			Revision: model.NewUserSettingsRevision(), CreatedAt: at, UpdatedAt: at,
		}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newSettingsServiceForTest(t, test.store, &settingsAuditFake{}, time.Now)
			_, err := service.ReadOwnUserSettings(context.Background(), invocation.New(principal, model.RequestMetadata{}))
			require.True(t, apperror.Is(err, "user_settings.unavailable"), "ReadOwn() error = %v", err)
			if err != nil {
				require.False(t, containsSensitiveText(err.Error(), secret))
			}
		})
	}
}

func TestUserSettingsReplaceOwnValidatesAndPreparesSafeAtomicMutation(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	principal := userSettingsSessionPrincipal(at)
	current := &model.UserSettingsDocument{
		UserID: principal.UserID, Source: "{}\n", FormatVersion: 1,
		Revision: model.NewUserSettingsRevision(), CreatedAt: at.Add(-time.Hour), UpdatedAt: at.Add(-time.Minute),
	}
	source := "{\n  // visual preference\n  \"workbench.colorTheme\": \"Proctor Dark\",\n}\n"
	persistence := &userSettingsStoreFake{document: current}
	audit := &settingsAuditFake{event: &model.AuditEvent{Action: "user.settings.replace"}}
	service := newSettingsServiceForTest(t, persistence, audit, func() time.Time { return at })
	nextRevision := model.NewUserSettingsRevision()
	persistence.replaceResult = &store.UserSettingsReplacementResult{
		Revision: nextRevision, FormatVersion: 1, UpdatedAt: at, Changed: true,
	}
	result, err := service.ReplaceOwnUserSettings(context.Background(), invocation.New(principal, model.RequestMetadata{}), ReplaceOwnUserSettingsCommand{
		Source: source, FormatVersion: 1, ExpectedRevision: current.Revision,
		IdempotencyKey: "settings-save-1",
	})
	require.NoError(t, err)
	require.Equal(t, nextRevision, result.Revision)
	require.True(t, result.Changed)
	require.False(t, result.Replayed)
	require.NotNil(t, persistence.replaceInput)
	require.Equal(t, principal.UserID, persistence.replaceInput.UserID)
	require.Equal(t, source, persistence.replaceInput.Source)
	require.Equal(t, 1, persistence.replaceInput.FormatVersion)
	require.Equal(t, current.Revision, persistence.replaceInput.ExpectedRevision)
	require.True(t, persistence.replaceInput.NextRevision.IsValid())
	require.True(t, persistence.replaceInput.UpdatedAt.Equal(at))
	require.NotNil(t, persistence.replaceCommand)
	require.Equal(t, "user_settings.replace", persistence.replaceCommand.Operation)
	require.Equal(t, principal.UserID, persistence.replaceCommand.UserID)
	require.Equal(t, 1, audit.calls, "audit input = %#v calls", audit.input)
	require.Len(t, source, audit.input.SourceBytes, "audit input = %#v calls", audit.input)
	require.Equal(t, current.Revision, audit.input.PreviousRevision, "audit input = %#v calls", audit.input)
	require.Equal(t, persistence.replaceInput.NextRevision, audit.input.ResultingRevision, "audit input = %#v calls", audit.input)
}

func TestUserSettingsReplaceOwnRejectsInvalidSourceWithoutLeakOrMutation(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	principal := userSettingsSessionPrincipal(at)
	secret := "secret.visual.key"
	persistence := &userSettingsStoreFake{}
	audit := &settingsAuditFake{}
	service := newSettingsServiceForTest(t, persistence, audit, time.Now)
	_, err := service.ReplaceOwnUserSettings(context.Background(), invocation.New(principal, model.RequestMetadata{}), ReplaceOwnUserSettingsCommand{
		Source: "{\"" + secret + "\": }", FormatVersion: 1,
		ExpectedRevision: model.NewUserSettingsRevision(), IdempotencyKey: "invalid-save",
	})
	require.True(t, apperror.Is(err, "user_settings.invalid"), "ReplaceOwn() error = %v", err)
	failure, ok := apperror.As(err)
	require.True(t, ok)
	require.NotEmpty(t, failure.Fields()["diagnostics"])
	require.False(t, containsSensitiveText(err.Error(), secret))
	require.Equal(t, 0, persistence.reads, "invalid source effects reads")
	require.Nil(t, persistence.replaceInput, "invalid source effects reads")
	require.Equal(t, 0, audit.calls, "invalid source effects reads")
}

func TestUserSettingsReplaceOwnRejectsUnsupportedFormatsWithoutEffects(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	principal := userSettingsSessionPrincipal(at)
	unsupported := &model.UserSettingsDocument{
		UserID: principal.UserID, Source: "future-format(private.value);\n", FormatVersion: 2,
		Revision: model.NewUserSettingsRevision(), CreatedAt: at.Add(-time.Hour), UpdatedAt: at,
	}
	tests := []struct {
		name          string
		persisted     *model.UserSettingsDocument
		targetVersion int
		wantReads     int
	}{
		{name: "unsupported persisted format", persisted: unsupported, targetVersion: 1, wantReads: 1},
		{name: "unsupported target format", persisted: unsupported, targetVersion: 2, wantReads: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			persistence := &userSettingsStoreFake{document: test.persisted}
			audit := &settingsAuditFake{}
			service := newSettingsServiceForTest(t, persistence, audit, time.Now)
			_, err := service.ReplaceOwnUserSettings(context.Background(), invocation.New(principal, model.RequestMetadata{}), ReplaceOwnUserSettingsCommand{
				Source: "{}\n", FormatVersion: test.targetVersion,
				ExpectedRevision: test.persisted.Revision, IdempotencyKey: "unsupported-format-save",
			})
			require.True(t, apperror.Is(err, "user_settings.format_unsupported"), "ReplaceOwn() error = %v", err)
			require.Equal(t, test.wantReads, persistence.reads, "effects reads")
			require.Nil(t, persistence.replaceInput, "effects reads")
			require.Nil(t, persistence.replaceCommand, "effects reads")
			require.Equal(t, 0, audit.calls, "effects reads")
			require.Equal(t, test.persisted.Source, persistence.document.Source)
			require.Equal(t, test.persisted.Revision, persistence.document.Revision)
			require.True(t, persistence.document.UpdatedAt.Equal(test.persisted.UpdatedAt))
		})
	}
}

func TestUserSettingsReplaceOwnMapsRevisionAndIdempotencyConflictsSafely(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	principal := userSettingsSessionPrincipal(at)
	current := &model.UserSettingsDocument{
		UserID: principal.UserID, Source: "{}\n", FormatVersion: 1,
		Revision: model.NewUserSettingsRevision(), CreatedAt: at.Add(-time.Hour), UpdatedAt: at.Add(-time.Minute),
	}
	for name, storeErr := range map[string]error{
		"revision":    &store.ErrUserSettingsRevisionConflict{CurrentRevision: current.Revision},
		"idempotency": &store.ErrIdempotencyConflict{},
	} {
		t.Run(name, func(t *testing.T) {
			persistence := &userSettingsStoreFake{document: current, replaceErr: storeErr}
			service := newSettingsServiceForTest(t,
				persistence,
				&settingsAuditFake{event: &model.AuditEvent{Action: "user.settings.replace"}},
				func() time.Time { return at },
			)
			_, err := service.ReplaceOwnUserSettings(context.Background(), invocation.New(principal, model.RequestMetadata{}), ReplaceOwnUserSettingsCommand{
				Source: "{\"editor.fontSize\": 16}\n", FormatVersion: 1,
				ExpectedRevision: current.Revision, IdempotencyKey: "conflict-key",
			})
			want := "user_settings.revision_conflict"
			if name == "idempotency" {
				want = "idempotency.conflict"
			}
			require.True(t, apperror.Is(err, want), "ReplaceOwn() error = %v, want %s", err, want)
			if name == "revision" {
				failure, _ := apperror.As(err)
				require.Equal(t, current.Revision.String(), failure.Fields()["current_revision"])
			}
		})
	}
}

func TestUserSettingsReplaceOwnUnknownOutcomeIsResolvedByAuthoritativeRead(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	principal := userSettingsSessionPrincipal(at)
	previous := &model.UserSettingsDocument{
		UserID: principal.UserID, Source: "{}\n", FormatVersion: 1,
		Revision: model.NewUserSettingsRevision(), CreatedAt: at.Add(-time.Hour), UpdatedAt: at.Add(-time.Minute),
	}
	persistence := &userSettingsStoreFake{
		document: previous, replaceErr: errors.New("commit outcome unavailable"),
	}
	service := newSettingsServiceForTest(t,
		persistence,
		&settingsAuditFake{event: &model.AuditEvent{Action: "user.settings.replace"}},
		func() time.Time { return at },
	)
	const source = "{\"editor.fontSize\": 18}\n"
	_, err := service.ReplaceOwnUserSettings(context.Background(), invocation.New(principal, model.RequestMetadata{}), ReplaceOwnUserSettingsCommand{
		Source: source, FormatVersion: 1, ExpectedRevision: previous.Revision,
		IdempotencyKey: "unknown-outcome",
	})
	require.True(t, apperror.Is(err, "user_settings.unavailable"), "ReplaceOwn() error = %v", err)
	committed := *previous
	committed.Source = source
	committed.Revision = model.NewUserSettingsRevision()
	committed.UpdatedAt = at
	persistence.document = &committed
	view, err := service.ReadOwnUserSettings(context.Background(), invocation.New(principal, model.RequestMetadata{}))
	require.NoError(t, err)
	require.Equal(t, source, view.Source)
	require.Equal(t, committed.Revision, view.Revision)
}

func TestUserSettingsReplaceOwnPublishesOnePostCommitRefetchHint(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 15, 15, 0, 0, 0, time.UTC)
	principal := userSettingsSessionPrincipal(at)
	current := &model.UserSettingsDocument{
		UserID: principal.UserID, Source: "{}\n", FormatVersion: 1,
		Revision: model.NewUserSettingsRevision(), CreatedAt: at.Add(-time.Hour), UpdatedAt: at.Add(-time.Minute),
	}
	next := model.NewUserSettingsRevision()
	events := []string{}
	persistence := &userSettingsStoreFake{document: current, events: &events, replaceResult: &store.UserSettingsReplacementResult{
		Revision: next, FormatVersion: 1, UpdatedAt: at, Changed: true,
	}}
	effects := &effectsFake{events: &events}
	service, err := newSettingsService(persistence, &settingsAuditFake{event: &model.AuditEvent{Action: "user.settings.replace"}}, effects, func() time.Time { return at })
	require.NoError(t, err)

	result, err := service.ReplaceOwnUserSettings(context.Background(), invocation.New(principal, model.RequestMetadata{}), ReplaceOwnUserSettingsCommand{
		Source: "{\"editor.fontSize\": 16}\n", FormatVersion: 1,
		ExpectedRevision: current.Revision, IdempotencyKey: "notify-settings",
	})
	require.NoError(t, err)
	require.True(t, result.Changed, "result=%#v changes", result)
	require.False(t, result.Replayed, "result=%#v changes", result)
	require.Len(t, effects.changes, 1, "result=%#v changes", result)
	change := effects.changes[0]
	require.Equal(t, principal.UserID, change.UserID)
	require.Equal(t, next, change.Revision)
	require.Equal(t, 1, change.FormatVersion)
	require.True(t, change.ChangedAt.Equal(at))
	require.Len(t, events, 2)
	require.Equal(t, "store", events[0])
	require.Equal(t, "effect", events[1])
	require.Empty(t, effects.operations)
}

func TestUserSettingsReplaceOwnSuppressesNoOpReplayAndRejectedCommandEvents(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 15, 15, 0, 0, 0, time.UTC)
	principal := userSettingsSessionPrincipal(at)
	current := &model.UserSettingsDocument{
		UserID: principal.UserID, Source: "{}\n", FormatVersion: 1,
		Revision: model.NewUserSettingsRevision(), CreatedAt: at.Add(-time.Hour), UpdatedAt: at,
	}
	tests := []struct {
		name     string
		result   *store.UserSettingsReplacementResult
		storeErr error
		source   string
	}{
		{name: "no-op", result: &store.UserSettingsReplacementResult{Revision: current.Revision, FormatVersion: 1, UpdatedAt: at}},
		{name: "replay", result: &store.UserSettingsReplacementResult{Revision: current.Revision, FormatVersion: 1, UpdatedAt: at, Changed: true, Replayed: true}},
		{name: "revision conflict", storeErr: &store.ErrUserSettingsRevisionConflict{CurrentRevision: current.Revision}},
		{name: "validation failure", source: "{private.key: }"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			effects := &effectsFake{}
			persistence := &userSettingsStoreFake{document: current, replaceResult: test.result, replaceErr: test.storeErr}
			audit := &settingsAuditFake{err: errors.New("audit unavailable")}
			service, err := newSettingsService(persistence, audit, effects, func() time.Time { return at.Add(time.Second) })
			require.NoError(t, err)
			source := "{}\n"
			if test.source != "" {
				source = test.source
			}
			_, replaceErr := service.ReplaceOwnUserSettings(context.Background(), invocation.New(principal, model.RequestMetadata{}), ReplaceOwnUserSettingsCommand{
				Source: source, FormatVersion: 1, ExpectedRevision: current.Revision, IdempotencyKey: "suppressed-event",
			})
			if test.name == "no-op" || test.name == "replay" {
				require.NoError(t, replaceErr)
			}
			require.Equal(t, 0, audit.calls, "suppressed command prepared")
			require.Empty(t, effects.changes)
		})
	}
}

func TestUserSettingsReplaceOwnRejectedPrincipalPublishesNothing(t *testing.T) {
	t.Parallel()
	effects := &effectsFake{}
	persistence := &userSettingsStoreFake{}
	service, err := newSettingsService(persistence, &settingsAuditFake{}, effects, time.Now)
	require.NoError(t, err)
	principal := model.Principal{
		UserID: model.NewUserID(), CredentialID: model.PrincipalCredentialID(model.NewPersonalAccessTokenID()),
		CredentialType: model.CredentialPersonalAccessToken, AuthenticationMethod: "personal_access_token",
		ClientType: model.SessionClientCLI, CredentialScopes: []string{string(model.ActionUserView)},
	}
	_, err = service.ReplaceOwnUserSettings(context.Background(), invocation.New(principal, model.RequestMetadata{}), ReplaceOwnUserSettingsCommand{
		Source: "{}\n", FormatVersion: 1, ExpectedRevision: model.NewUserSettingsRevision(), IdempotencyKey: "rejected",
	})
	require.True(t, apperror.Is(err, "authentication.token_invalid"), "error=%v reads=%d replace=%#v changes", err, persistence.reads, persistence.replaceInput)
	require.Equal(t, 0, persistence.reads, "error=%v reads=%d replace=%#v changes", err, persistence.reads, persistence.replaceInput)
	require.Nil(t, persistence.replaceInput, "error=%v reads=%d replace=%#v changes", err, persistence.reads, persistence.replaceInput)
	require.Empty(t, effects.changes, "error=%v reads=%d replace=%#v changes", err, persistence.reads, persistence.replaceInput)
}

func TestUserSettingsRealtimeFailureDoesNotChangeCommittedResultAndReadReconciles(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 15, 15, 0, 0, 0, time.UTC)
	principal := userSettingsSessionPrincipal(at)
	current := &model.UserSettingsDocument{UserID: principal.UserID, Source: "{}\n", FormatVersion: 1,
		Revision: model.NewUserSettingsRevision(), CreatedAt: at.Add(-time.Hour), UpdatedAt: at.Add(-time.Minute)}
	next := model.NewUserSettingsRevision()
	persistence := &userSettingsStoreFake{document: current, replaceResult: &store.UserSettingsReplacementResult{
		Revision: next, FormatVersion: 1, UpdatedAt: at, Changed: true,
	}}
	failures := &effectsFake{err: errors.New("peer delivery unavailable")}
	service, err := newSettingsService(persistence, &settingsAuditFake{event: &model.AuditEvent{Action: "user.settings.replace"}}, failures, func() time.Time { return at })
	require.NoError(t, err)
	const source = "{\"workbench.colorTheme\": \"Dark\"}\n"
	result, err := service.ReplaceOwnUserSettings(context.Background(), invocation.New(principal, model.RequestMetadata{}), ReplaceOwnUserSettingsCommand{
		Source: source, FormatVersion: 1, ExpectedRevision: current.Revision, IdempotencyKey: "delivery-fails",
	})
	require.NoError(t, err, "ReplaceOwn() = %#v", result)
	require.Equal(t, next, result.Revision, "ReplaceOwn() = %#v", result)
	require.True(t, result.Changed, "ReplaceOwn() = %#v", result)
	require.Len(t, failures.operations, 1)
	require.Equal(t, "user_settings_changed", failures.operations[0])
	committed := *current
	committed.Source, committed.Revision, committed.UpdatedAt = source, next, at
	persistence.document = &committed
	view, err := service.ReadOwnUserSettings(context.Background(), invocation.New(principal, model.RequestMetadata{}))
	require.NoError(t, err, "authoritative read = %#v", view)
	require.Equal(t, source, view.Source, "authoritative read = %#v", view)
	require.Equal(t, next, view.Revision, "authoritative read = %#v", view)
}

type userSettingsRaceStore struct {
	store.UserSettingsStore
	document *model.UserSettingsDocument
	result   *store.UserSettingsReplacementResult
	calls    atomic.Int32
}

func (s *userSettingsRaceStore) Get(context.Context, model.UserID) (*model.UserSettingsDocument, error) {
	return s.document.Clone(), nil
}

func (s *userSettingsRaceStore) Replace(context.Context, *store.UserSettingsReplacement, *store.CommandIdempotency) (*store.UserSettingsReplacementResult, error) {
	if s.calls.Add(1) == 1 {
		return s.result, nil
	}
	return nil, &store.ErrUserSettingsRevisionConflict{CurrentRevision: s.result.Revision}
}

type userSettingsAtomicEffects struct {
	effectsBase
	calls atomic.Int32
}

func (e *userSettingsAtomicEffects) SettingsChanged(context.Context, SettingsChange) error {
	e.calls.Add(1)
	return nil
}

type userSettingsConcurrentAudit struct{}

func (userSettingsConcurrentAudit) PrepareReplacement(context.Context, access.UserSettingsAuditInput) (*model.AuditEvent, error) {
	return &model.AuditEvent{Action: "user.settings.replace"}, nil
}

func TestUserSettingsConcurrentWritersPublishOnlyTheCommittedWinner(t *testing.T) {
	at := time.Date(2026, 8, 15, 15, 0, 0, 0, time.UTC)
	principal := userSettingsSessionPrincipal(at)
	current := &model.UserSettingsDocument{UserID: principal.UserID, Source: "{}\n", FormatVersion: 1,
		Revision: model.NewUserSettingsRevision(), CreatedAt: at.Add(-time.Hour), UpdatedAt: at.Add(-time.Minute)}
	persistence := &userSettingsRaceStore{document: current, result: &store.UserSettingsReplacementResult{
		Revision: model.NewUserSettingsRevision(), FormatVersion: 1, UpdatedAt: at, Changed: true,
	}}
	effects := &userSettingsAtomicEffects{}
	service, err := newSettingsService(persistence, userSettingsConcurrentAudit{}, effects, func() time.Time { return at })
	require.NoError(t, err)
	start := make(chan struct{})
	errorsByWriter := make(chan error, 2)
	for index := 0; index < 2; index++ {
		go func(index int) {
			<-start
			_, replaceErr := service.ReplaceOwnUserSettings(context.Background(), invocation.New(principal, model.RequestMetadata{}), ReplaceOwnUserSettingsCommand{
				Source: "{\"editor.fontSize\": " + string(rune('1'+index)) + "}\n", FormatVersion: 1,
				ExpectedRevision: current.Revision, IdempotencyKey: "racing-writer-" + string(rune('a'+index)),
			})
			errorsByWriter <- replaceErr
		}(index)
	}
	close(start)
	successes := 0
	for index := 0; index < 2; index++ {
		if err := <-errorsByWriter; err == nil {
			successes++
		}
	}
	require.Equal(t, 1, successes, "successes")
	require.Equal(t, int32(1), effects.calls.Load(), "successes")
}

func containsSensitiveText(value, sensitive string) bool {
	for index := 0; index+len(sensitive) <= len(value); index++ {
		if value[index:index+len(sensitive)] == sensitive {
			return true
		}
	}
	return false
}

func newSettingsService(settings store.UserSettingsStore, audit SettingsAuditor, effects Effects, now func() time.Time) (*Service, error) {
	return New(Dependencies{Store: &catalogFake{userSettings: settings}, SettingsAuditor: audit, Effects: effects, Now: now})
}

func newSettingsServiceForTest(t *testing.T, settings store.UserSettingsStore, audit SettingsAuditor, now func() time.Time) *Service {
	t.Helper()
	service, err := newSettingsService(settings, audit, &effectsFake{}, now)
	require.NoError(t, err)
	return service
}
