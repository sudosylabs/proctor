// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package user

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// UserSettingsView is the exact current self-owned settings source plus the
// metadata a client needs for compatibility and optimistic concurrency.
type UserSettingsView struct {
	Source        string
	FormatVersion int
	Revision      model.UserSettingsRevision
	Writable      bool
	UpdatedAt     time.Time
}

// UserSettingsReplacementResult reports the stored document after a
// replacement and whether it changed it or replayed an earlier one.
type UserSettingsReplacementResult struct {
	Revision      model.UserSettingsRevision
	FormatVersion int
	UpdatedAt     time.Time
	Changed       bool
	Replayed      bool
}

// ReplaceOwnUserSettingsCommand replaces the caller's settings source when
// ExpectedRevision still names the stored revision.
type ReplaceOwnUserSettingsCommand struct {
	Source           string
	FormatVersion    int
	ExpectedRevision model.UserSettingsRevision
	IdempotencyKey   string
}

// UserSettingsDiagnostic locates one reason a settings source was refused.
type UserSettingsDiagnostic struct {
	Code   string `json:"code"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

// ReplaceOwnUserSettings replaces the caller's own settings document when the
// expected revision is current, and is idempotent per key.
func (s *Service) ReplaceOwnUserSettings(
	ctx context.Context,
	inv invocation.Invocation,
	command ReplaceOwnUserSettingsCommand,
) (UserSettingsReplacementResult, error) {
	principal := inv.Principal()
	if principal.Validate() != nil || principal.CredentialType != model.CredentialSessionAccess {
		return UserSettingsReplacementResult{}, apperror.New("authentication.token_invalid")
	}
	if command.IdempotencyKey == "" {
		return UserSettingsReplacementResult{}, apperror.New("idempotency.key_required")
	}
	if command.FormatVersion != model.UserSettingsFormatVersion1 {
		return UserSettingsReplacementResult{}, apperror.New("user_settings.format_unsupported")
	}
	if !command.ExpectedRevision.IsValid() {
		return UserSettingsReplacementResult{}, apperror.New("request.invalid").WithField("field", "expected_revision")
	}

	diagnostics := validateUserSettingsSource(command.Source)
	if len(diagnostics) != 0 {
		public := make([]UserSettingsDiagnostic, len(diagnostics))
		for index, diagnostic := range diagnostics {
			public[index] = UserSettingsDiagnostic{
				Code:   diagnostic.Code,
				Line:   diagnostic.Line,
				Column: diagnostic.Column,
			}
		}

		encoded, _ := json.Marshal(public)
		return UserSettingsReplacementResult{}, apperror.New("user_settings.invalid").WithField("diagnostics", string(encoded))
	}

	current, err := s.settings.Get(ctx, principal.UserID)
	if err != nil || current == nil || current.UserID != principal.UserID || current.Validate() != nil {
		if err == nil {
			err = errors.New("invalid persisted settings document")
		}
		return UserSettingsReplacementResult{}, userSettingsUnavailable(err)
	}

	if current.FormatVersion != model.UserSettingsFormatVersion1 {
		return UserSettingsReplacementResult{}, apperror.New("user_settings.format_unsupported")
	}

	nextRevision := model.NewUserSettingsRevision()
	updatedAt := model.TimeUTC(s.now())
	if !updatedAt.After(current.UpdatedAt) {
		updatedAt = current.UpdatedAt.Add(time.Millisecond)
	}

	idempotency, err := idempotency.Prepare(
		inv,
		"user_settings.replace",
		command.IdempotencyKey,
		struct {
			Source           string                     `json:"source"`
			FormatVersion    int                        `json:"format_version"`
			ExpectedRevision model.UserSettingsRevision `json:"expected_revision"`
		}{command.Source, command.FormatVersion, command.ExpectedRevision},
	)
	if err != nil {
		return UserSettingsReplacementResult{}, err
	}

	var audit *model.AuditEvent
	if current.Revision == command.ExpectedRevision &&
		(current.Source != command.Source || current.FormatVersion != command.FormatVersion) {
		audit, err = s.settingsAuditor.PrepareReplacement(ctx, access.UserSettingsAuditInput{
			Invocation:        inv,
			UserID:            principal.UserID,
			PreviousRevision:  current.Revision,
			ResultingRevision: nextRevision,
			FormatVersion:     command.FormatVersion,
			SourceBytes:       len(command.Source),
		})
		if err != nil {
			return UserSettingsReplacementResult{}, err
		}
	}

	stored, err := s.settings.Replace(ctx, &store.UserSettingsReplacement{
		UserID:           principal.UserID,
		Source:           command.Source,
		FormatVersion:    command.FormatVersion,
		ExpectedRevision: command.ExpectedRevision,
		NextRevision:     nextRevision,
		UpdatedAt:        updatedAt,
		AuditEvent:       audit,
	}, idempotency)
	if err != nil {
		return UserSettingsReplacementResult{}, userSettingsReplacementError(err)
	}
	if stored == nil || !stored.Revision.IsValid() || stored.FormatVersion <= 0 || stored.UpdatedAt.IsZero() {
		return UserSettingsReplacementResult{}, userSettingsUnavailable(errors.New("invalid settings replacement result"))
	}

	result := UserSettingsReplacementResult{
		Revision:      stored.Revision,
		FormatVersion: stored.FormatVersion,
		UpdatedAt:     stored.UpdatedAt,
		Changed:       stored.Changed,
		Replayed:      stored.Replayed,
	}
	if result.Changed && !result.Replayed {
		if effectErr := s.publishSettingsChanged(ctx, principal.UserID, result); effectErr != nil {
			s.publisher.Report(ctx, "user_settings_changed", effectErr)
		}
	}

	return result, nil
}

func userSettingsReplacementError(err error) error {
	var revision *store.ErrUserSettingsRevisionConflict
	if errors.As(err, &revision) {
		return apperror.New("user_settings.revision_conflict").WithField("current_revision", revision.CurrentRevision.String()).Wrap(err)
	}
	return userSettingsStoreErrors.Translate(err)
}

var userSettingsStoreErrors = apperror.StoreTable{
	Invalid:     "user_settings.invalid",
	Unavailable: "user_settings.unavailable",
}

// ReadOwnUserSettings returns only the document owned by the interactive Session
// principal. There is intentionally no caller-supplied User identifier.
func (s *Service) ReadOwnUserSettings(ctx context.Context, inv invocation.Invocation) (UserSettingsView, error) {
	principal := inv.Principal()
	if principal.Validate() != nil || principal.CredentialType != model.CredentialSessionAccess {
		return UserSettingsView{}, apperror.New("authentication.token_invalid")
	}
	document, err := s.settings.Get(ctx, principal.UserID)
	if err != nil {
		return UserSettingsView{}, userSettingsUnavailable(err)
	}
	if document == nil || document.UserID != principal.UserID || document.Validate() != nil {
		return UserSettingsView{}, userSettingsUnavailable(errors.New("invalid persisted settings metadata"))
	}
	writable := document.FormatVersion == model.UserSettingsFormatVersion1
	if writable && len(validateUserSettingsSource(document.Source)) != 0 {
		return UserSettingsView{}, userSettingsUnavailable(errors.New("invalid persisted settings source"))
	}
	return UserSettingsView{
		Source:        document.Source,
		FormatVersion: document.FormatVersion,
		Revision:      document.Revision,
		Writable:      writable,
		UpdatedAt:     document.UpdatedAt,
	}, nil
}

func userSettingsUnavailable(err error) error {
	return apperror.New("user_settings.unavailable").Wrap(err)
}

// PrepareInitialSettingsDocument builds the settings document a newly created
// User starts with, to be stored in the same step that creates the User.
func PrepareInitialSettingsDocument(user *model.User) (*model.UserSettingsDocument, error) {
	if user == nil || !user.ID.IsValid() || user.CreatedAt.IsZero() {
		return nil, errors.New("prepared user is required for initial settings")
	}
	return model.NewUserSettingsDocument(user.ID, model.NewUserSettingsRevision(), user.CreatedAt)
}
