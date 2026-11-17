// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// SQLAssistantProfileStore implements store.AssistantProfileStore on
// assistant_profiles.
type SQLAssistantProfileStore struct {
	*SQLStore
}

func newSQLAssistantProfileStore(sqlStore *SQLStore) store.AssistantProfileStore {
	return &SQLAssistantProfileStore{SQLStore: sqlStore}
}

type assistantProfileSpecificationDocument model.AssistantProfileSpecification

func (assistantProfileSpecificationDocument) outcomeDocument() {}

type assistantProfileRow struct {
	ID              string       `db:"id"`
	Name            string       `db:"name"`
	Specification   []byte       `db:"specification"`
	CreatedByUserID string       `db:"created_by_user_id"`
	CreatedAt       time.Time    `db:"created_at"`
	RetiredAt       sql.NullTime `db:"retired_at"`
	Revision        int64        `db:"revision"`
}

const assistantProfileColumns = `id, name, specification, created_by_user_id, created_at, retired_at, revision`

func (r assistantProfileRow) model() (*model.AssistantProfile, error) {
	var document assistantProfileSpecificationDocument
	if err := decodeOutcome(r.Specification, &document); err != nil {
		return nil, invalidPersistedState("assistant_profile", "specification", err)
	}
	spec := model.AssistantProfileSpecification(document)
	profile := &model.AssistantProfile{ID: model.AssistantProfileID(r.ID), AssistantProfileSpecification: spec,
		CreatedByUserID: model.UserID(r.CreatedByUserID), CreatedAt: model.TimeUTC(r.CreatedAt), Revision: r.Revision}
	if r.RetiredAt.Valid {
		profile.RetiredAt = model.TimeUTC(r.RetiredAt.Time)
	}
	if spec.Name != r.Name {
		return nil, invalidPersistedState("assistant_profile", "name", errors.New("inconsistent name"))
	}
	if err := profile.Validate(); err != nil {
		return nil, invalidPersistedState("assistant_profile", "value", err)
	}
	return profile, nil
}

type assistantProfileDocument struct {
	ID              string                              `json:"id"`
	Specification   model.AssistantProfileSpecification `json:"specification"`
	CreatedByUserID string                              `json:"created_by_user_id"`
	CreatedAt       time.Time                           `json:"created_at"`
	RetiredAt       time.Time                           `json:"retired_at,omitzero"`
	Revision        int64                               `json:"revision"`
}
type assistantProfileOutcome struct {
	Profile assistantProfileDocument `json:"profile"`
	Changed bool                     `json:"changed"`
}

func (assistantProfileOutcome) outcomeDocument() {}
func assistantProfileOutcomeFrom(p *model.AssistantProfile, changed bool) assistantProfileOutcome {
	return assistantProfileOutcome{Profile: assistantProfileDocument{ID: p.ID.String(), Specification: p.AssistantProfileSpecification,
		CreatedByUserID: p.CreatedByUserID.String(), CreatedAt: p.CreatedAt, RetiredAt: p.RetiredAt, Revision: p.Revision}, Changed: changed}
}
func (o assistantProfileOutcome) result(replayed bool) (*store.AssistantProfileCommandResult, error) {
	p := &model.AssistantProfile{ID: model.AssistantProfileID(o.Profile.ID), AssistantProfileSpecification: o.Profile.Specification,
		CreatedByUserID: model.UserID(o.Profile.CreatedByUserID), CreatedAt: o.Profile.CreatedAt, RetiredAt: o.Profile.RetiredAt, Revision: o.Profile.Revision}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &store.AssistantProfileCommandResult{Profile: p, Changed: o.Changed, Replayed: replayed}, nil
}
func decodeAssistantProfileOutcome(encoded []byte) (assistantProfileOutcome, error) {
	var outcome assistantProfileOutcome
	if err := decodeOutcome(encoded, &outcome); err != nil {
		return outcome, err
	}
	if _, err := outcome.result(false); err != nil {
		return outcome, invalidPersistedState("command_outcome", "outcome", err)
	}
	return outcome, nil
}

// Create saves a new profile for an active system administrator.
func (s SQLAssistantProfileStore) Create(
	ctx context.Context,
	input *store.AssistantProfileCreation,
	command *store.CommandIdempotency,
) (*store.AssistantProfileCommandResult, error) {
	if input == nil || input.Profile == nil || input.Profile.Validate() != nil || input.Profile.Retired() ||
		!input.AuditEventID.IsValid() || input.AuditAt.IsZero() || command == nil ||
		command.UserID != input.Profile.CreatedByUserID || command.Operation != store.AssistantProfileCreateOperation {
		return nil, store.NewErrInvalidInput("assistant_profile", "creation", nil)
	}
	mutation, err := runIdempotentMutation(ctx, s.SQLStore, "assistant profile creation",
		idempotentMutation[assistantProfileOutcome]{
			command:      command,
			auditEventID: input.AuditEventID,
			execute: func(ctx context.Context, tx *sqlxTxWrapper) (assistantProfileOutcome, error) {
				return createAssistantProfile(ctx, tx, input)
			},
			encode: func(outcome assistantProfileOutcome) ([]byte, error) { return encodeOutcome(outcome) },
			decode: decodeAssistantProfileOutcome,
			completeReplay: func(ctx context.Context, tx *sqlxTxWrapper, outcome assistantProfileOutcome, originalAuditID string) error {
				if _, err := requireSystemAdministrator(ctx, tx, input.Profile.CreatedByUserID); err != nil {
					return err
				}
				return completeAssistantProfileAudit(ctx, tx, "create", outcome, input.AuditEventID, input.AuditAt, true, originalAuditID)
			},
		})
	if err != nil {
		return nil, err
	}
	return mutation.Value.result(mutation.Replayed)
}

func createAssistantProfile(
	ctx context.Context,
	tx *sqlxTxWrapper,
	input *store.AssistantProfileCreation,
) (assistantProfileOutcome, error) {
	databaseNow, err := requireSystemAdministrator(ctx, tx, input.Profile.CreatedByUserID)
	if err != nil {
		return assistantProfileOutcome{}, err
	}
	var live int
	if err = tx.Get(ctx, &live, `SELECT COUNT(*) FROM assistant_profiles WHERE retired_at IS NULL`); err != nil {
		return assistantProfileOutcome{}, fmt.Errorf("count assistant profiles: %w", err)
	}
	if live >= model.AssistantProfileListMaximum {
		return assistantProfileOutcome{}, store.NewErrConflict("assistant_profile", "assistant_profile_limit", nil)
	}
	profile := *input.Profile
	profile.CreatedAt = databaseNow
	specification, err := json.Marshal(profile.AssistantProfileSpecification)
	if err != nil {
		return assistantProfileOutcome{}, fmt.Errorf("encode assistant profile: %w", err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO assistant_profiles (`+assistantProfileColumns+`)
  VALUES (?, ?, ?::jsonb, ?, ?, NULL, ?)`, profile.ID.String(), profile.Name, string(specification),
		profile.CreatedByUserID.String(), profile.CreatedAt, profile.Revision); err != nil {
		translated := translateError("assistant_profile", profile.ID.String(), err)
		var conflict *store.ErrConflict
		if errors.As(translated, &conflict) && conflict.Constraint == "assistant_profiles_live_name_key" {
			return assistantProfileOutcome{}, store.NewErrConflict("assistant_profile", "assistant_profile_name", err)
		}
		return assistantProfileOutcome{}, fmt.Errorf("insert assistant profile: %w", translated)
	}
	outcome := assistantProfileOutcomeFrom(&profile, true)
	if err = completeAssistantProfileAudit(ctx, tx, "create", outcome, input.AuditEventID, input.AuditAt, false, ""); err != nil {
		return assistantProfileOutcome{}, err
	}
	return outcome, nil
}

// Retire retires one profile for an active system administrator.
func (s SQLAssistantProfileStore) Retire(
	ctx context.Context,
	input *store.AssistantProfileRetirement,
	command *store.CommandIdempotency,
) (*store.AssistantProfileCommandResult, error) {
	if input == nil || !input.ActorID.IsValid() || !input.ProfileID.IsValid() || !input.AuditEventID.IsValid() ||
		input.AuditAt.IsZero() || command == nil || command.UserID != input.ActorID ||
		command.Operation != store.AssistantProfileRetireOperation {
		return nil, store.NewErrInvalidInput("assistant_profile", "retirement", nil)
	}
	mutation, err := runIdempotentMutation(ctx, s.SQLStore, "assistant profile retirement",
		idempotentMutation[assistantProfileOutcome]{
			command:      command,
			auditEventID: input.AuditEventID,
			execute: func(ctx context.Context, tx *sqlxTxWrapper) (assistantProfileOutcome, error) {
				return retireAssistantProfile(ctx, tx, input)
			},
			encode: func(outcome assistantProfileOutcome) ([]byte, error) { return encodeOutcome(outcome) },
			decode: decodeAssistantProfileOutcome,
			completeReplay: func(ctx context.Context, tx *sqlxTxWrapper, outcome assistantProfileOutcome, originalAuditID string) error {
				if _, err := requireSystemAdministrator(ctx, tx, input.ActorID); err != nil {
					return err
				}
				return completeAssistantProfileAudit(ctx, tx, "retire", outcome, input.AuditEventID, input.AuditAt, true, originalAuditID)
			},
		})
	if err != nil {
		return nil, err
	}
	return mutation.Value.result(mutation.Replayed)
}

func retireAssistantProfile(
	ctx context.Context,
	tx *sqlxTxWrapper,
	input *store.AssistantProfileRetirement,
) (assistantProfileOutcome, error) {
	databaseNow, err := requireSystemAdministrator(ctx, tx, input.ActorID)
	if err != nil {
		return assistantProfileOutcome{}, err
	}
	var row assistantProfileRow
	if err = tx.Get(ctx, &row, `SELECT `+assistantProfileColumns+` FROM assistant_profiles WHERE id = ? FOR UPDATE`,
		input.ProfileID.String()); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return assistantProfileOutcome{}, store.NewErrNotFound("assistant_profile", input.ProfileID.String())
		}
		return assistantProfileOutcome{}, fmt.Errorf("lock assistant profile: %w", err)
	}
	profile, err := row.model()
	if err != nil {
		return assistantProfileOutcome{}, err
	}
	changed := !profile.Retired()
	if changed {
		profile.Revision++
		profile.RetiredAt = transitionInstant(databaseNow, profile.CreatedAt, profile.CreatedAt)
		if _, err = tx.Exec(ctx, `UPDATE assistant_profiles SET retired_at = ?, revision = ? WHERE id = ? AND retired_at IS NULL`,
			profile.RetiredAt, profile.Revision, profile.ID.String()); err != nil {
			return assistantProfileOutcome{}, fmt.Errorf("retire assistant profile: %w", err)
		}
	}
	outcome := assistantProfileOutcomeFrom(profile, changed)
	if err = completeAssistantProfileAudit(ctx, tx, "retire", outcome, input.AuditEventID, input.AuditAt, false, ""); err != nil {
		return assistantProfileOutcome{}, err
	}
	return outcome, nil
}

func completeAssistantProfileAudit(
	ctx context.Context,
	tx *sqlxTxWrapper,
	operation string,
	outcome assistantProfileOutcome,
	auditEventID model.AuditEventID,
	auditAt time.Time,
	replayed bool,
	originalAuditID string,
) error {
	data := map[string]any{
		"operation":            operation,
		"assistant_profile_id": outcome.Profile.ID,
		"name":                 outcome.Profile.Specification.Name,
		"provider":             outcome.Profile.Specification.Provider,
		"changed":              outcome.Changed,
	}
	if replayed {
		data["idempotency_replayed"] = true
		data["original_audit_event_id"] = originalAuditID
	}
	encoded, err := model.EncodeAuditData(data)
	if err != nil {
		return store.NewErrInvalidInput("assistant_profile", "audit", nil).Wrap(err)
	}
	if _, err = completeAuditEvent(ctx, tx, auditEventID, model.AuditStatusSuccess, "", encoded, auditAt); err != nil {
		return fmt.Errorf("complete assistant profile audit: %w", err)
	}
	return nil
}

// Get returns one profile, retired or not.
func (s SQLAssistantProfileStore) Get(ctx context.Context, id model.AssistantProfileID) (*model.AssistantProfile, error) {
	if !id.IsValid() {
		return nil, store.NewErrInvalidInput("assistant_profile", "id", nil)
	}
	var row assistantProfileRow
	if err := s.GetMaster().Get(ctx, &row, `SELECT `+assistantProfileColumns+` FROM assistant_profiles WHERE id = ?`,
		id.String()); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.NewErrNotFound("assistant_profile", id.String())
		}
		return nil, fmt.Errorf("get assistant profile: %w", err)
	}
	return row.model()
}

// List returns the profiles that are not retired by name, then, when asked,
// the retired ones, newest retirement first.
func (s SQLAssistantProfileStore) List(ctx context.Context, includeRetired bool) ([]*model.AssistantProfile, error) {
	query := `SELECT ` + assistantProfileColumns + ` FROM assistant_profiles WHERE retired_at IS NULL ORDER BY name`
	if includeRetired {
		query = `SELECT ` + assistantProfileColumns + ` FROM assistant_profiles
			ORDER BY retired_at IS NOT NULL, CASE WHEN retired_at IS NULL THEN name END, retired_at DESC, id`
	}
	var rows []assistantProfileRow
	if err := s.GetMaster().Select(ctx, &rows, query); err != nil {
		return nil, fmt.Errorf("list assistant profiles: %w", err)
	}
	profiles := make([]*model.AssistantProfile, 0, len(rows))
	for _, row := range rows {
		profile, err := row.model()
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, nil
}

var _ store.AssistantProfileStore = (*SQLAssistantProfileStore)(nil)
