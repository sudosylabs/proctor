// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// SQLDesktopCompatibilityPolicyStore implements
// store.DesktopCompatibilityPolicyStore on the singleton
// desktop_compatibility_policies row, fenced by its revision.
type SQLDesktopCompatibilityPolicyStore struct {
	*SQLStore
}

type desktopCompatibilityPolicyRow struct {
	InstitutionID         string     `db:"institution_id"`
	Revision              int64      `db:"revision"`
	MinimumDesktopRelease string     `db:"minimum_desktop_release"`
	RevokedDesktopBuilds  jsonValue  `db:"revoked_desktop_builds"`
	AdministratorMessage  string     `db:"administrator_message"`
	Availability          string     `db:"availability"`
	RetryAt               *time.Time `db:"retry_at"`
	CreatedAt             time.Time  `db:"created_at"`
	UpdatedAt             time.Time  `db:"updated_at"`
}

// desktopCompatibilityPolicyReplacementOutcome is the committed result of a
// Desktop compatibility policy replacement.
type desktopCompatibilityPolicyReplacementOutcome struct {
	Policy  *model.DesktopCompatibilityPolicy
	Changed bool
}

// desktopCompatibilityPolicyReplacementDocument is its persisted outcome.
type desktopCompatibilityPolicyReplacementDocument struct {
	Policy  desktopCompatibilityPolicyDocument `json:"policy"`
	Changed bool                               `json:"changed"`
}

func (desktopCompatibilityPolicyReplacementDocument) outcomeDocument() {}

// desktopCompatibilityPolicyDocument spells model.DesktopCompatibilityPolicy
// inside a persisted outcome; the two convert directly.
type desktopCompatibilityPolicyDocument struct {
	InstitutionID         model.InstitutionID              `json:"institution_id"`
	Revision              int64                            `json:"revision"`
	MinimumDesktopRelease string                           `json:"minimum_desktop_release"`
	RevokedDesktopBuilds  []desktopBuildRevocationDocument `json:"revoked_desktop_builds"`
	AdministratorMessage  string                           `json:"administrator_message"`
	Availability          model.DesktopAvailability        `json:"availability"`
	RetryAt               model.OptionalTime               `json:"retry_at"`
	CreatedAt             time.Time                        `json:"created_at"`
	UpdatedAt             time.Time                        `json:"updated_at"`
}

// desktopBuildRevocationDocument spells model.DesktopBuildRevocation in the
// policy row's revoked_desktop_builds and in a persisted outcome.
type desktopBuildRevocationDocument struct {
	BuildID         string       `json:"build_id"`
	Reason          string       `json:"reason"`
	RevokedAt       time.Time    `json:"revoked_at"`
	RevokedByUserID model.UserID `json:"revoked_by_user_id"`
}

func newDesktopCompatibilityPolicyDocument(policy *model.DesktopCompatibilityPolicy) desktopCompatibilityPolicyDocument {
	return desktopCompatibilityPolicyDocument{
		InstitutionID:         policy.InstitutionID,
		Revision:              policy.Revision,
		MinimumDesktopRelease: policy.MinimumDesktopRelease,
		RevokedDesktopBuilds:  desktopBuildRevocationDocuments(policy.RevokedDesktopBuilds),
		AdministratorMessage:  policy.AdministratorMessage,
		Availability:          policy.Availability,
		RetryAt:               policy.RetryAt,
		CreatedAt:             policy.CreatedAt,
		UpdatedAt:             policy.UpdatedAt,
	}
}

func (d desktopCompatibilityPolicyDocument) model() *model.DesktopCompatibilityPolicy {
	return &model.DesktopCompatibilityPolicy{
		InstitutionID:         d.InstitutionID,
		Revision:              d.Revision,
		MinimumDesktopRelease: d.MinimumDesktopRelease,
		RevokedDesktopBuilds:  desktopBuildRevocations(d.RevokedDesktopBuilds),
		AdministratorMessage:  d.AdministratorMessage,
		Availability:          d.Availability,
		RetryAt:               d.RetryAt,
		CreatedAt:             d.CreatedAt,
		UpdatedAt:             d.UpdatedAt,
	}
}

func desktopBuildRevocationDocuments(revocations []model.DesktopBuildRevocation) []desktopBuildRevocationDocument {
	documents := make([]desktopBuildRevocationDocument, 0, len(revocations))
	for _, revocation := range revocations {
		documents = append(documents, desktopBuildRevocationDocument{
			BuildID: revocation.BuildID, Reason: revocation.Reason,
			RevokedAt: model.TimeUTC(revocation.RevokedAt), RevokedByUserID: revocation.RevokedByUserID,
		})
	}
	return documents
}

func desktopBuildRevocations(documents []desktopBuildRevocationDocument) []model.DesktopBuildRevocation {
	revocations := make([]model.DesktopBuildRevocation, 0, len(documents))
	for _, document := range documents {
		revocations = append(revocations, model.DesktopBuildRevocation{
			BuildID: document.BuildID, Reason: document.Reason,
			RevokedAt: model.TimeUTC(document.RevokedAt), RevokedByUserID: document.RevokedByUserID,
		})
	}
	return revocations
}

func newSQLDesktopCompatibilityPolicyStore(sqlStore *SQLStore) store.DesktopCompatibilityPolicyStore {
	return &SQLDesktopCompatibilityPolicyStore{SQLStore: sqlStore}
}

func (s SQLDesktopCompatibilityPolicyStore) Get(
	ctx context.Context,
) (*model.DesktopCompatibilityPolicy, error) {
	return getDesktopCompatibilityPolicy(ctx, s.GetMaster(), "")
}

func (s SQLDesktopCompatibilityPolicyStore) Replace(
	ctx context.Context,
	input *store.DesktopCompatibilityPolicyReplacement,
	command *store.CommandIdempotency,
) (*store.DesktopCompatibilityPolicyReplacementResult, error) {
	if err := validateDesktopCompatibilityPolicyReplacement(input, command); err != nil {
		return nil, err
	}

	mutation, err := runIdempotentMutation(
		ctx,
		s.SQLStore,
		"desktop compatibility policy replacement",
		idempotentMutation[desktopCompatibilityPolicyReplacementOutcome]{
			command:      command,
			auditEventID: input.AuditEventID,
			execute: func(
				ctx context.Context,
				tx *sqlxTxWrapper,
			) (desktopCompatibilityPolicyReplacementOutcome, error) {
				return replaceDesktopCompatibilityPolicy(ctx, tx, input)
			},
			encode: func(outcome desktopCompatibilityPolicyReplacementOutcome) ([]byte, error) {
				if outcome.Policy == nil {
					return nil, store.NewErrInvalidInput("desktop_compatibility_policy", "outcome", nil)
				}
				return encodeOutcome(desktopCompatibilityPolicyReplacementDocument{
					Policy:  newDesktopCompatibilityPolicyDocument(outcome.Policy),
					Changed: outcome.Changed,
				})
			},
			decode: decodeDesktopCompatibilityPolicyReplacementOutcome,
			completeReplay: func(
				ctx context.Context,
				tx *sqlxTxWrapper,
				outcome desktopCompatibilityPolicyReplacementOutcome,
				originalAuditID string,
			) error {
				return completeDesktopCompatibilityPolicyAudit(
					ctx,
					tx,
					input,
					outcome,
					true,
					originalAuditID,
				)
			},
		},
	)
	if err != nil {
		return nil, err
	}

	return &store.DesktopCompatibilityPolicyReplacementResult{
		Policy:   mutation.Value.Policy,
		Changed:  mutation.Value.Changed,
		Replayed: mutation.Replayed,
	}, nil
}

// replaceDesktopCompatibilityPolicy locks the policy, requires an active
// system administrator and the expected revision, applies the replacement at
// PostgreSQL time, and completes the audit.
func replaceDesktopCompatibilityPolicy(
	ctx context.Context,
	tx *sqlxTxWrapper,
	input *store.DesktopCompatibilityPolicyReplacement,
) (desktopCompatibilityPolicyReplacementOutcome, error) {
	if err := lockSystemAdministratorAuthenticationPaths(ctx, tx); err != nil {
		return desktopCompatibilityPolicyReplacementOutcome{}, err
	}

	current, err := getDesktopCompatibilityPolicy(ctx, tx, "FOR UPDATE")
	if err != nil {
		return desktopCompatibilityPolicyReplacementOutcome{}, err
	}

	// Read wall time after serialization; transaction-start time can
	// precede a binding revocation committed while this edit waited.
	var databaseNow time.Time
	if err = tx.Get(ctx, &databaseNow, `SELECT clock_timestamp()`); err != nil {
		return desktopCompatibilityPolicyReplacementOutcome{}, fmt.Errorf(
			"read desktop compatibility policy mutation time: %w",
			err,
		)
	}

	administrator, err := isActiveSystemAdministrator(
		ctx,
		tx,
		input.ActorID.String(),
		databaseNow,
	)
	if err != nil {
		return desktopCompatibilityPolicyReplacementOutcome{}, err
	}

	if !administrator {
		return desktopCompatibilityPolicyReplacementOutcome{}, store.NewErrConflict(
			"desktop_compatibility_policy",
			"actor_not_system_administrator",
			nil,
		)
	}

	if current.Revision != input.ExpectedRevision {
		return desktopCompatibilityPolicyReplacementOutcome{},
			&store.ErrDesktopCompatibilityPolicyRevisionConflict{CurrentRevision: current.Revision}
	}

	candidate := current.Clone()
	// The recorded change never precedes a policy lifecycle stamped
	// by a leading node clock; PostgreSQL time decided authority.
	at := transitionInstant(databaseNow, current.CreatedAt, current.UpdatedAt)
	if err = candidate.Replace(input.ExpectedRevision, input.Settings, input.ActorID, at); err != nil {
		return desktopCompatibilityPolicyReplacementOutcome{}, store.NewErrInvalidInput(
			"desktop_compatibility_policy",
			"replacement",
			nil,
		).Wrap(err)
	}

	changed := candidate.Revision != current.Revision
	if changed {
		if err = updateDesktopCompatibilityPolicy(ctx, tx, candidate, current.Revision); err != nil {
			return desktopCompatibilityPolicyReplacementOutcome{}, err
		}
	}

	outcome := desktopCompatibilityPolicyReplacementOutcome{
		Policy:  candidate,
		Changed: changed,
	}
	if err = completeDesktopCompatibilityPolicyAudit(
		ctx,
		tx,
		input,
		outcome,
		false,
		"",
	); err != nil {
		return desktopCompatibilityPolicyReplacementOutcome{}, err
	}

	return outcome, nil
}

// updateDesktopCompatibilityPolicy writes the replaced policy over the row at
// its previous revision.
func updateDesktopCompatibilityPolicy(
	ctx context.Context,
	tx *sqlxTxWrapper,
	candidate *model.DesktopCompatibilityPolicy,
	previousRevision int64,
) error {
	encodedBuildIDs, encodeErr := json.Marshal(desktopBuildRevocationDocuments(candidate.RevokedDesktopBuilds))
	if encodeErr != nil {
		return store.NewErrInvalidInput(
			"desktop_compatibility_policy",
			"revoked_build_ids",
			nil,
		).Wrap(encodeErr)
	}

	result, updateErr := tx.Exec(
		ctx,
		`
		UPDATE desktop_compatibility_policies
		   SET revision = ?, minimum_desktop_release = ?, revoked_desktop_builds = ?::jsonb,
		       administrator_message = ?, availability = ?, retry_at = ?, updated_at = ?
		 WHERE singleton = 1 AND revision = ?`,
		candidate.Revision,
		candidate.MinimumDesktopRelease,
		encodedBuildIDs,
		candidate.AdministratorMessage,
		candidate.Availability,
		candidate.RetryAt.Ptr(),
		candidate.UpdatedAt,
		previousRevision,
	)
	if updateErr != nil {
		return fmt.Errorf(
			"replace desktop compatibility policy: %w",
			translateError(
				"desktop_compatibility_policy",
				candidate.InstitutionID.String(),
				updateErr,
			),
		)
	}

	return requireDesktopCompatibilityPolicyRevisionAffected(result)
}

func requireDesktopCompatibilityPolicyRevisionAffected(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read desktop compatibility policy affected rows: %w", err)
	}
	if affected == 1 {
		return nil
	}
	return store.NewErrConflict(
		"desktop_compatibility_policy",
		"desktop_compatibility_policy_changed",
		nil,
	)
}

func getDesktopCompatibilityPolicy(
	ctx context.Context,
	executor sqlxExecutor,
	lock string,
) (*model.DesktopCompatibilityPolicy, error) {
	query := `SELECT institution_id, revision, minimum_desktop_release,
		revoked_desktop_builds, administrator_message, availability, retry_at, created_at, updated_at
		FROM desktop_compatibility_policies WHERE singleton = 1`
	if lock != "" {
		query += " " + lock
	}
	var row desktopCompatibilityPolicyRow
	if err := executor.Get(ctx, &row, query); err != nil {
		return nil, translateError("desktop_compatibility_policy", "singleton", err)
	}
	return row.model()
}

func requireDesktopCompatibilityPolicyRevision(
	ctx context.Context,
	executor sqlxExecutor,
	expected int64,
) error {
	if expected < 1 {
		return store.NewErrInvalidInput("desktop_compatibility_policy", "revision", nil)
	}
	policy, err := getDesktopCompatibilityPolicy(ctx, executor, "FOR SHARE")
	if err != nil {
		return err
	}
	if policy.Revision != expected {
		return store.NewErrConflict("desktop_compatibility_policy", "desktop_compatibility_policy_revision", nil)
	}
	return nil
}

func (r desktopCompatibilityPolicyRow) model() (*model.DesktopCompatibilityPolicy, error) {
	institutionID, err := model.ParseInstitutionID(r.InstitutionID)
	if err != nil {
		return nil, invalidPersistedState("desktop_compatibility_policy", "institution_id", err)
	}
	revoked := []desktopBuildRevocationDocument{}
	decoder := json.NewDecoder(bytes.NewReader(r.RevokedDesktopBuilds))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&revoked); err != nil {
		return nil, invalidPersistedState("desktop_compatibility_policy", "revoked_desktop_builds", err)
	}
	policy := &model.DesktopCompatibilityPolicy{
		InstitutionID:         institutionID,
		Revision:              r.Revision,
		MinimumDesktopRelease: r.MinimumDesktopRelease,
		RevokedDesktopBuilds:  desktopBuildRevocations(revoked),
		AdministratorMessage:  r.AdministratorMessage,
		Availability:          model.DesktopAvailability(r.Availability),
		RetryAt:               model.OptionalTimeFromPtr(r.RetryAt),
		CreatedAt:             model.TimeUTC(r.CreatedAt),
		UpdatedAt:             model.TimeUTC(r.UpdatedAt),
	}
	if err := policy.Validate(); err != nil {
		return nil, invalidPersistedState("desktop_compatibility_policy", "value", err)
	}
	return policy, nil
}

func validateDesktopCompatibilityPolicyReplacement(
	input *store.DesktopCompatibilityPolicyReplacement,
	command *store.CommandIdempotency,
) error {
	if input == nil ||
		!input.ActorID.IsValid() ||
		input.ExpectedRevision < 1 ||
		input.Settings.Validate() != nil ||
		!input.AuditEventID.IsValid() ||
		input.AuditAt.IsZero() ||
		command == nil ||
		command.UserID != input.ActorID ||
		command.Operation != "desktop_compatibility_policy.replace" ||
		command.Retention <= 0 ||
		command.Wait <= 0 {
		return store.NewErrInvalidInput("desktop_compatibility_policy", "replacement", nil)
	}
	return nil
}

func decodeDesktopCompatibilityPolicyReplacementOutcome(encoded []byte) (desktopCompatibilityPolicyReplacementOutcome, error) {
	var document desktopCompatibilityPolicyReplacementDocument
	err := decodeOutcome(encoded, &document)
	outcome := desktopCompatibilityPolicyReplacementOutcome{Policy: document.Policy.model(), Changed: document.Changed}
	if err != nil || outcome.Policy.Validate() != nil {
		return desktopCompatibilityPolicyReplacementOutcome{}, invalidPersistedState(
			"command_outcome",
			"outcome",
			errors.New("invalid desktop compatibility policy outcome"),
		)
	}
	return outcome, nil
}

func completeDesktopCompatibilityPolicyAudit(
	ctx context.Context,
	tx *sqlxTxWrapper,
	input *store.DesktopCompatibilityPolicyReplacement,
	outcome desktopCompatibilityPolicyReplacementOutcome,
	replayed bool,
	originalAuditID string,
) error {
	data := map[string]any{
		"operation":                 "replace",
		"expected_revision":         input.ExpectedRevision,
		"resulting_revision":        outcome.Policy.Revision,
		"changed":                   outcome.Changed,
		"minimum_desktop_release":   outcome.Policy.MinimumDesktopRelease,
		"revoked_build_count":       len(outcome.Policy.RevokedDesktopBuilds),
		"administrator_message_set": outcome.Policy.AdministratorMessage != "",
		"availability":              outcome.Policy.Availability,
		"retry_at_set":              outcome.Policy.RetryAt.Valid,
	}
	if replayed {
		data["idempotency_replayed"] = true
		data["original_audit_event_id"] = originalAuditID
	}

	encoded, err := model.EncodeAuditData(data)
	if err != nil {
		return store.NewErrInvalidInput("desktop_compatibility_policy", "audit", nil).Wrap(err)
	}

	if _, err := completeAuditEvent(
		ctx,
		tx,
		input.AuditEventID,
		model.AuditStatusSuccess,
		"",
		encoded,
		input.AuditAt,
	); err != nil {
		return fmt.Errorf("complete desktop compatibility policy audit: %w", err)
	}

	return nil
}

var _ store.DesktopCompatibilityPolicyStore = (*SQLDesktopCompatibilityPolicyStore)(nil)

// CountOpenSittings reads at most one row past maximum.
func (s SQLDesktopCompatibilityPolicyStore) CountOpenSittings(ctx context.Context, maximum int) (int, error) {
	if !store.ValidCountMaximum(maximum) {
		return 0, store.NewErrInvalidInput("desktop_compatibility_policy", "open_sittings", nil)
	}
	return countBoundedSQL(ctx, s.GetMaster(),
		`SELECT 1 FROM exam_sittings WHERE state IN ('open', 'paused', 'closing')`, nil, maximum, "open sittings")
}
