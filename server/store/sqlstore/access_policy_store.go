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
	"sort"
	"time"

	"github.com/lib/pq"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type SQLAccessPolicyStore struct{ *SQLStore }

func newSQLAccessPolicyStore(sqlStore *SQLStore) store.AccessPolicyStore {
	return &SQLAccessPolicyStore{SQLStore: sqlStore}
}

type accessPolicyRow struct {
	ID                               string        `db:"id"`
	Revision                         int64         `db:"revision"`
	CreatedAt                        time.Time     `db:"created_at"`
	UpdatedAt                        time.Time     `db:"updated_at"`
	LocalLoginEnabled                bool          `db:"local_login_enabled"`
	PublicRegistrationEnabled        bool          `db:"public_registration_enabled"`
	InvitationAdmissionEnabled       bool          `db:"invitation_admission_enabled"`
	InvitationLocalCredentialEnabled bool          `db:"invitation_local_credential_enabled"`
	DesktopAuthorizationEnabled      bool          `db:"desktop_authorization_enabled"`
	ProviderAdmissions               jsonValue     `db:"provider_admissions"`
	RoleHolderMFARequired            bool          `db:"role_holder_mfa_required"`
	SessionIdleMinutes               sql.NullInt64 `db:"session_idle_minutes"`
	SessionAbsoluteMinutes           sql.NullInt64 `db:"session_absolute_minutes"`
}

type accessPolicyTransitionRow struct {
	PolicyID      string         `db:"access_policy_id"`
	FromRevision  int64          `db:"from_revision"`
	ToRevision    int64          `db:"to_revision"`
	ActorID       string         `db:"actor_user_id"`
	ChangedFields pq.StringArray `db:"changed_fields"`
	ChangedAt     time.Time      `db:"changed_at"`
	Outcome       string         `db:"outcome"`
}

func (s SQLAccessPolicyStore) Get(ctx context.Context, historyLimit int) (*store.AccessPolicySnapshot, error) {
	if historyLimit < 0 || historyLimit > model.AccessPolicyTransitionHistoryLimit {
		return nil, store.NewErrInvalidInput("access_policy", "history_limit", historyLimit)
	}
	policy, err := getAccessPolicy(ctx, s.GetMaster(), "")
	if err != nil {
		return nil, err
	}
	history, err := listAccessPolicyHistory(ctx, s.GetMaster(), policy.ID, historyLimit)
	if err != nil {
		return nil, err
	}
	return &store.AccessPolicySnapshot{Policy: policy, History: history}, nil
}

// Preflight reports the blockers of a proposed replacement at the expected
// revision, with the role holder two-step verification counts, at one database
// instant. It changes nothing.
func (s SQLAccessPolicyStore) Preflight(
	ctx context.Context,
	input *store.AccessPolicyPreflight,
) (*store.AccessPolicyPreflightResult, error) {
	if err := validateAccessPolicyPreflight(input); err != nil {
		return nil, err
	}
	return runSQLTransaction(ctx, s.GetMaster().Begin, "access policy preflight", func(
		ctx context.Context,
		tx *sqlxTxWrapper,
	) (*store.AccessPolicyPreflightResult, error) {
		current, err := getAccessPolicy(ctx, tx, "FOR SHARE")
		if err != nil {
			return nil, err
		}
		if current.Revision != input.ExpectedRevision {
			return nil, &store.ErrAccessPolicyRevisionConflict{CurrentRevision: current.Revision}
		}

		var databaseNow time.Time
		if getErr := tx.Get(ctx, &databaseNow, `SELECT clock_timestamp()`); getErr != nil {
			return nil, fmt.Errorf("read access policy preflight time: %w", getErr)
		}

		blockers, err := accessPolicyBlockers(ctx, tx, current, input.Settings, input.Capabilities, databaseNow)
		if err != nil {
			return nil, err
		}

		result := &store.AccessPolicyPreflightResult{Blockers: blockers}
		if err := tx.Get(ctx, &result.RoleHoldersWithoutMFACount, `SELECT COUNT(*)
			FROM users u
			WHERE `+roleHolderUserPredicate+` AND NOT EXISTS (
				SELECT 1
				FROM mfa_credentials mc
				WHERE mc.user_id = u.id
					AND mc.archived_at IS NULL
					AND mc.state = 'active'
			)`, databaseNow); err != nil {
			return nil, fmt.Errorf("count role holders without MFA: %w", err)
		}

		if err := tx.Get(ctx, &result.RoleHolderSingleFactorSessionCount, `SELECT COUNT(*)
			FROM sessions s
			JOIN users u ON u.id = s.user_id
			WHERE `+roleHolderSingleFactorSessionPredicate+` AND `+roleHolderUserPredicate, databaseNow); err != nil {
			return nil, fmt.Errorf("count role holder single-factor sessions: %w", err)
		}

		return result, nil
	})
}

// roleHolderUserPredicate selects an active User u holding, at the instant bound
// to $1, a current Role Binding of an unarchived Role whose Academic Unit or
// Class scope is not archived. A binding at any scope makes a User a role holder.
const roleHolderUserPredicate = `u.archived_at IS NULL
		AND u.disabled_at IS NULL
		AND EXISTS (
			SELECT 1
			FROM role_bindings rb
			JOIN roles r ON r.id = rb.role_id
			WHERE rb.user_id = u.id
				AND rb.archived_at IS NULL
				AND r.archived_at IS NULL
				AND rb.start_at <= $1
				AND (rb.end_at IS NULL OR rb.end_at > $1)
				AND (
					rb.scope_type = 'institution'
					OR (
						rb.scope_type = 'academic_unit'
						AND EXISTS (
							SELECT 1
							FROM academic_units au
							WHERE au.id = rb.scope_id
								AND au.archived_at IS NULL
						)
					)
					OR (
						rb.scope_type = 'class'
						AND EXISTS (
							SELECT 1
							FROM classes c
							WHERE c.id = rb.scope_id
								AND c.archived_at IS NULL
						)
					)
				)
		)`

// roleHolderSingleFactorSessionPredicate selects the Sessions s that are live at
// the instant bound to $1 and have not proved a second factor.
const roleHolderSingleFactorSessionPredicate = `s.archived_at IS NULL
		AND s.revoked_at IS NULL
		AND s.expires_at > $1
		AND s.idle_expires_at > $1
		AND s.authentication_strength <> 'multi_factor'`

// HasRoleHolderMFARequirement reports whether the current Access Policy requires
// two-step verification of role holders and the User is a role holder at the given instant.
func (s SQLAccessPolicyStore) HasRoleHolderMFARequirement(ctx context.Context, userID model.UserID, at time.Time) (bool, error) {
	if !userID.IsValid() || at.IsZero() {
		return false, store.NewErrInvalidInput("access_policy", "has_role_holder_mfa_requirement", nil)
	}
	var required bool
	if err := s.GetMaster().Get(ctx, &required, `SELECT EXISTS (
			SELECT 1
			FROM access_policies p
			JOIN users u ON u.id = $2
			WHERE p.singleton = 1
				AND p.role_holder_mfa_required
				AND `+roleHolderUserPredicate+`)`, model.TimeUTC(at), userID.String()); err != nil {
		return false, fmt.Errorf("check access policy MFA requirement: %w", err)
	}
	return required, nil
}

// accessPolicyReplacementOutcome is the committed result of an Access Policy
// replacement. It persists as accessPolicyReplacementDocument.
type accessPolicyReplacementOutcome struct {
	Result              *store.AccessPolicyReplacementResult
	ChangedFields       []string
	RevokedSessionCount int
}

// accessPolicyReplacementDocument is the persisted outcome of an Access
// Policy replacement. Revocation identifiers and token hashes are post-commit
// effects, not part of the retained response, so replays never emit them.
type accessPolicyReplacementDocument struct {
	Policy              accessPolicyDocument             `json:"policy"`
	History             []accessPolicyTransitionDocument `json:"history"`
	Changed             bool                             `json:"changed"`
	ChangedFields       []string                         `json:"changed_fields"`
	RevokedSessionCount int                              `json:"revoked_session_count"`
}

func (accessPolicyReplacementDocument) outcomeDocument() {}

// accessPolicyDocument spells model.AccessPolicy inside a persisted outcome;
// the two convert directly.
type accessPolicyDocument struct {
	ID                               model.AccessPolicyID                   `json:"id"`
	Revision                         int64                                  `json:"revision"`
	CreatedAt                        time.Time                              `json:"created_at"`
	UpdatedAt                        time.Time                              `json:"updated_at"`
	LocalLoginEnabled                bool                                   `json:"local_login_enabled"`
	PublicRegistrationEnabled        bool                                   `json:"public_registration_enabled"`
	InvitationAdmissionEnabled       bool                                   `json:"invitation_admission_enabled"`
	InvitationLocalCredentialEnabled bool                                   `json:"invitation_local_credential_enabled"`
	DesktopAuthorizationEnabled      bool                                   `json:"desktop_authorization_enabled"`
	ProviderAdmissions               map[string]model.ProviderAdmissionMode `json:"provider_admissions"`
	RoleHolderMFARequired            bool                                   `json:"role_holder_mfa_required"`
	SessionIdleMinutes               int                                    `json:"session_idle_minutes"`
	SessionAbsoluteMinutes           int                                    `json:"session_absolute_minutes"`
}

// accessPolicyTransitionDocument spells model.AccessPolicyTransition inside a
// persisted outcome; the two convert directly.
type accessPolicyTransitionDocument struct {
	PolicyID      model.AccessPolicyID                `json:"policy_id"`
	FromRevision  int64                               `json:"from_revision"`
	ToRevision    int64                               `json:"to_revision"`
	ActorID       model.UserID                        `json:"actor_id"`
	ChangedFields []string                            `json:"changed_fields"`
	ChangedAt     time.Time                           `json:"changed_at"`
	Outcome       model.AccessPolicyTransitionOutcome `json:"outcome"`
}

func encodeAccessPolicyReplacementOutcome(outcome accessPolicyReplacementOutcome) ([]byte, error) {
	if outcome.Result == nil || outcome.Result.Snapshot == nil || outcome.Result.Snapshot.Policy == nil {
		return nil, store.NewErrInvalidInput("access_policy", "outcome", nil)
	}

	snapshot := outcome.Result.Snapshot
	document := accessPolicyReplacementDocument{
		Policy:              accessPolicyDocument(*snapshot.Policy),
		Changed:             outcome.Result.Changed,
		ChangedFields:       outcome.ChangedFields,
		RevokedSessionCount: outcome.RevokedSessionCount,
	}
	if snapshot.History != nil {
		document.History = make([]accessPolicyTransitionDocument, 0, len(snapshot.History))
	}

	for _, transition := range snapshot.History {
		if transition == nil {
			return nil, store.NewErrInvalidInput("access_policy", "outcome_history", nil)
		}

		document.History = append(document.History, accessPolicyTransitionDocument(*transition))
	}

	return encodeOutcome(document)
}

// Replace applies a preflighted Access Policy replacement at the expected
// revision as an idempotent mutation, optionally revoking Sessions of disabled
// methods.
func (s SQLAccessPolicyStore) Replace(
	ctx context.Context,
	input *store.AccessPolicyReplacement,
	command *store.CommandIdempotency,
) (*store.AccessPolicyReplacementResult, error) {
	if err := validateAccessPolicyReplacement(input, command); err != nil {
		return nil, err
	}

	mutation, err := runIdempotentMutation(ctx, s.SQLStore, "access policy replacement", idempotentMutation[accessPolicyReplacementOutcome]{
		command:      command,
		auditEventID: input.AuditEventID,
		execute: func(ctx context.Context, tx *sqlxTxWrapper) (accessPolicyReplacementOutcome, error) {
			return replaceAccessPolicy(ctx, tx, input)
		},
		encode: encodeAccessPolicyReplacementOutcome,
		decode: decodeAccessPolicyReplacementOutcome,
		completeReplay: func(ctx context.Context, tx *sqlxTxWrapper, outcome accessPolicyReplacementOutcome, originalAuditID string) error {
			return completeAccessPolicyReplacementAudit(ctx, tx, outcome, input, true, originalAuditID)
		},
	})
	if err != nil {
		return nil, err
	}

	result := mutation.Value.Result
	result.Replayed = mutation.Replayed
	return result, nil
}

// replaceAccessPolicy applies a preflighted Access Policy replacement inside the idempotent mutation transaction.
func replaceAccessPolicy(
	ctx context.Context,
	tx *sqlxTxWrapper,
	input *store.AccessPolicyReplacement,
) (accessPolicyReplacementOutcome, error) {
	current, databaseNow, err := lockUnblockedAccessPolicy(ctx, tx, input)
	if err != nil {
		return accessPolicyReplacementOutcome{}, err
	}

	candidate := current.Clone()
	// PostgreSQL time decided the blockers; the recorded transition never
	// precedes a policy lifecycle stamped by a leading node clock.
	at := transitionInstant(databaseNow, current.CreatedAt, current.UpdatedAt)
	transition, err := candidate.Replace(current.Revision, input.Preflight.Settings, input.ActorID, at)
	if err != nil {
		return accessPolicyReplacementOutcome{}, store.NewErrInvalidInput("access_policy", "replacement", nil).Wrap(err)
	}

	changed := transition != nil
	revocations := []store.AccessPolicySessionRevocation{}
	deadlineChanges := []store.AccessPolicySessionDeadlineChange{}
	if changed && input.Preflight.RevokeExistingSessions {
		transition.ChangedFields = append(transition.ChangedFields, "revoke_existing_sessions")
		sort.Strings(transition.ChangedFields)
		if err = transition.Validate(); err != nil {
			return accessPolicyReplacementOutcome{}, store.NewErrInvalidInput("access_policy", "transition", nil).Wrap(err)
		}

		revocations, err = revokeSessionsForDisabledAccessMethods(ctx, tx, current, candidate, databaseNow)
		if err != nil {
			return accessPolicyReplacementOutcome{}, err
		}

		if candidate.RoleHolderMFARequired && !current.RoleHolderMFARequired {
			roleHolderRevocations, roleHolderErr := revokeRoleHolderSingleFactorSessions(ctx, tx, databaseNow)
			if roleHolderErr != nil {
				return accessPolicyReplacementOutcome{}, roleHolderErr
			}

			revocations = mergeAccessPolicySessionRevocations(revocations, roleHolderRevocations)
		}
	}

	if changed {
		if err = persistAccessPolicyReplacement(ctx, tx, current, candidate, transition); err != nil {
			return accessPolicyReplacementOutcome{}, err
		}

		deadlineChanges, err = shortenLiveSessionDeadlines(ctx, tx, current, candidate, input.Preflight.SessionLimits, databaseNow)
		if err != nil {
			return accessPolicyReplacementOutcome{}, err
		}
	}

	changedFields := []string{}
	if transition != nil {
		changedFields = append(changedFields, transition.ChangedFields...)
	}

	history, err := listAccessPolicyHistory(ctx, tx, candidate.ID, model.AccessPolicyTransitionHistoryLimit)
	if err != nil {
		return accessPolicyReplacementOutcome{}, err
	}

	outcome := accessPolicyReplacementOutcome{Result: &store.AccessPolicyReplacementResult{
		Snapshot:               &store.AccessPolicySnapshot{Policy: candidate, History: history},
		Changed:                changed,
		SessionRevocations:     revocations,
		SessionDeadlineChanges: deadlineChanges,
	}, ChangedFields: changedFields, RevokedSessionCount: accessPolicyRevokedSessionCount(revocations)}
	if err := completeAccessPolicyReplacementAudit(ctx, tx, outcome, input, false, ""); err != nil {
		return accessPolicyReplacementOutcome{}, err
	}

	return outcome, nil
}

// lockUnblockedAccessPolicy locks the current Access Policy at the expected revision and requires the replacement to have no blockers.
func lockUnblockedAccessPolicy(
	ctx context.Context,
	tx *sqlxTxWrapper,
	input *store.AccessPolicyReplacement,
) (*model.AccessPolicy, time.Time, error) {
	if err := lockSystemAdministratorAuthenticationPaths(ctx, tx); err != nil {
		return nil, time.Time{}, err
	}

	current, err := getAccessPolicy(ctx, tx, "FOR UPDATE")
	if err != nil {
		return nil, time.Time{}, err
	}
	if current.Revision != input.Preflight.ExpectedRevision {
		return nil, time.Time{}, &store.ErrAccessPolicyRevisionConflict{CurrentRevision: current.Revision}
	}

	var databaseNow time.Time
	if err = tx.Get(ctx, &databaseNow, `SELECT clock_timestamp()`); err != nil {
		return nil, time.Time{}, fmt.Errorf("read access policy mutation time: %w", err)
	}

	blockers, err := accessPolicyBlockers(ctx, tx, current, input.Preflight.Settings, input.Preflight.Capabilities, databaseNow)
	if err != nil {
		return nil, time.Time{}, err
	}
	if len(blockers) != 0 {
		return nil, time.Time{}, &store.ErrAccessPolicyBlocked{Blockers: blockers}
	}

	return current, databaseNow, nil
}

// persistAccessPolicyReplacement writes the changed Access Policy, records its transition and bounds the transition history.
func persistAccessPolicyReplacement(
	ctx context.Context,
	tx *sqlxTxWrapper,
	current, candidate *model.AccessPolicy,
	transition *model.AccessPolicyTransition,
) error {
	providers, encodeErr := json.Marshal(candidate.ProviderAdmissions)
	if encodeErr != nil {
		return store.NewErrInvalidInput("access_policy", "provider_admissions", nil).Wrap(encodeErr)
	}

	result, updateErr := tx.Exec(
		ctx,
		`UPDATE access_policies
		SET revision = ?, updated_at = ?, local_login_enabled = ?, public_registration_enabled = ?,
			invitation_admission_enabled = ?, invitation_local_credential_enabled = ?,
			desktop_authorization_enabled = ?, provider_admissions = ?::jsonb,
			role_holder_mfa_required = ?, session_idle_minutes = ?, session_absolute_minutes = ?
		WHERE singleton = 1
			AND revision = ?`,
		candidate.Revision,
		candidate.UpdatedAt,
		candidate.LocalLoginEnabled,
		candidate.PublicRegistrationEnabled,
		candidate.InvitationAdmissionEnabled,
		candidate.InvitationLocalCredentialEnabled,
		candidate.DesktopAuthorizationEnabled,
		providers,
		candidate.RoleHolderMFARequired,
		nullableMinutes(candidate.SessionIdleMinutes),
		nullableMinutes(candidate.SessionAbsoluteMinutes),
		current.Revision,
	)
	if updateErr != nil {
		return fmt.Errorf("replace access policy: %w", translateError("access_policy", candidate.ID.String(), updateErr))
	}
	if err := requireRevisionAffected(ctx, tx, result, "access_policy", "access_policies", candidate.ID.String()); err != nil {
		return err
	}
	if err := insertAccessPolicyTransition(ctx, tx, transition); err != nil {
		return err
	}

	if _, err := tx.Exec(
		ctx,
		`DELETE FROM access_policy_transitions
		WHERE access_policy_id = ?
			AND to_revision NOT IN (
				SELECT to_revision
				FROM access_policy_transitions
				WHERE access_policy_id = ?
				ORDER BY to_revision DESC
				LIMIT ?
			)`,
		candidate.ID.String(),
		candidate.ID.String(),
		model.AccessPolicyTransitionHistoryLimit,
	); err != nil {
		return fmt.Errorf("bound access policy transition history: %w", err)
	}

	return nil
}

func decodeAccessPolicyReplacementOutcome(encoded []byte) (accessPolicyReplacementOutcome, error) {
	var document accessPolicyReplacementDocument
	if err := decodeOutcome(encoded, &document); err != nil {
		return accessPolicyReplacementOutcome{}, invalidPersistedState("command_outcome", "outcome", err)
	}

	policy := model.AccessPolicy(document.Policy)
	snapshot := &store.AccessPolicySnapshot{Policy: &policy}
	if document.History != nil {
		snapshot.History = make([]*model.AccessPolicyTransition, 0, len(document.History))
	}

	for _, transition := range document.History {
		value := model.AccessPolicyTransition(transition)
		snapshot.History = append(snapshot.History, &value)
	}

	outcome := accessPolicyReplacementOutcome{
		Result: &store.AccessPolicyReplacementResult{
			Snapshot:               snapshot,
			Changed:                document.Changed,
			SessionRevocations:     []store.AccessPolicySessionRevocation{},
			SessionDeadlineChanges: []store.AccessPolicySessionDeadlineChange{},
		},
		ChangedFields:       document.ChangedFields,
		RevokedSessionCount: document.RevokedSessionCount,
	}
	if outcome.Result.Snapshot.Policy.Validate() != nil ||
		outcome.RevokedSessionCount < 0 ||
		len(outcome.ChangedFields) > model.AccessPolicyTransitionFieldMaxCount {
		return accessPolicyReplacementOutcome{}, invalidPersistedState("command_outcome", "outcome", errors.New("invalid access policy outcome"))
	}

	for _, transition := range outcome.Result.Snapshot.History {
		if transition == nil || transition.Validate() != nil {
			return accessPolicyReplacementOutcome{}, invalidPersistedState(
				"command_outcome",
				"outcome",
				errors.New("invalid access policy history outcome"),
			)
		}
	}

	return outcome, nil
}

func completeAccessPolicyReplacementAudit(
	ctx context.Context,
	tx *sqlxTxWrapper,
	outcome accessPolicyReplacementOutcome,
	input *store.AccessPolicyReplacement,
	replayed bool,
	originalAuditID string,
) error {
	data := map[string]any{
		"operation":                "replace",
		"expected_revision":        input.Preflight.ExpectedRevision,
		"resulting_revision":       outcome.Result.Snapshot.Policy.Revision,
		"changed_fields":           append([]string(nil), outcome.ChangedFields...),
		"revoke_existing_sessions": input.Preflight.RevokeExistingSessions,
		"revoked_session_count":    outcome.RevokedSessionCount,
	}
	if replayed {
		data["idempotency_replayed"] = true
		data["original_audit_event_id"] = originalAuditID
	}

	encoded, err := model.EncodeAuditData(data)
	if err != nil {
		return store.NewErrInvalidInput("access_policy", "audit", nil).Wrap(err)
	}
	if _, err = completeAuditEvent(ctx, tx, input.AuditEventID, model.AuditStatusSuccess, "", encoded, input.AuditAt); err != nil {
		return fmt.Errorf("complete access policy replacement audit: %w", err)
	}

	return nil
}

func disabledAccessPolicyAuthenticationMethods(current, candidate *model.AccessPolicy) (bool, []string) {
	providers := make([]string, 0, len(current.ProviderAdmissions))
	for providerID := range current.ProviderAdmissions {
		if _, enabled := candidate.ProviderAdmissions[providerID]; !enabled {
			providers = append(providers, providerID)
		}
	}
	sort.Strings(providers)
	return current.LocalLoginEnabled && !candidate.LocalLoginEnabled, providers
}

type accessPolicySessionCredentialRow struct {
	UserID    string `db:"user_id"`
	SessionID string `db:"session_id"`
	TokenHash string `db:"token_hash"`
}

func revokeSessionsForDisabledAccessMethods(
	ctx context.Context,
	tx *sqlxTxWrapper,
	current, candidate *model.AccessPolicy,
	at time.Time,
) ([]store.AccessPolicySessionRevocation, error) {
	localDisabled, providerIDs := disabledAccessPolicyAuthenticationMethods(current, candidate)
	if !localDisabled && len(providerIDs) == 0 {
		return []store.AccessPolicySessionRevocation{}, nil
	}

	// AuthenticationMethod identifies the protocol (password, oidc, cas), while
	// AuthenticationProviderID identifies the immutable configured provider.
	// Never infer provider identity from the protocol: provider IDs may collide
	// with protocol or local-method names.
	providerArray := pq.Array(providerIDs)
	var sessionRows []struct {
		UserID    string `db:"user_id"`
		SessionID string `db:"session_id"`
	}
	if err := tx.Select(ctx, &sessionRows, `SELECT user_id, id AS session_id
		FROM sessions
		WHERE archived_at IS NULL
			AND revoked_at IS NULL
			AND expires_at > ?
			AND idle_expires_at > ?
			AND (
				(? AND authentication_method = 'password' AND authentication_provider_id = '')
				OR authentication_provider_id = ANY(?)
			)
		ORDER BY user_id, id
		FOR UPDATE`, at, at, localDisabled, providerArray); err != nil {
		return nil, fmt.Errorf("select access-policy sessions for revocation: %w", err)
	}

	if len(sessionRows) == 0 {
		return []store.AccessPolicySessionRevocation{}, nil
	}

	credentialRows := []accessPolicySessionCredentialRow{}
	if err := tx.Select(ctx, &credentialRows, `SELECT s.user_id, s.id AS session_id, c.token_hash
		FROM sessions s
		JOIN session_credentials c ON c.session_id = s.id
		WHERE s.archived_at IS NULL
			AND s.revoked_at IS NULL
			AND s.expires_at > ?
			AND s.idle_expires_at > ?
			AND (
				(? AND s.authentication_method = 'password' AND s.authentication_provider_id = '')
				OR s.authentication_provider_id = ANY(?)
			)
			AND c.kind = 'access'
			AND c.archived_at IS NULL
			AND c.revoked_at IS NULL
		ORDER BY s.user_id, s.id`, at, at, localDisabled, providerArray); err != nil {
		return nil, fmt.Errorf("select access-policy session credentials for revocation: %w", err)
	}

	// Sessions and credentials carry the creating node's clock while this
	// revocation uses PostgreSQL time. A node clock that leads the database
	// must not produce a revocation that precedes the row's creation.
	if _, err := tx.Exec(ctx, `UPDATE session_credentials c
		SET updated_at = GREATEST(c.updated_at, ?), revoked_at = GREATEST(c.created_at, ?)
		FROM sessions s
		WHERE s.id = c.session_id
			AND s.archived_at IS NULL
			AND s.revoked_at IS NULL
			AND s.expires_at > ?
			AND s.idle_expires_at > ?
			AND (
				(? AND s.authentication_method = 'password' AND s.authentication_provider_id = '')
				OR s.authentication_provider_id = ANY(?)
			)
			AND c.archived_at IS NULL
			AND c.revoked_at IS NULL`, at, at, at, at, localDisabled, providerArray); err != nil {
		return nil, fmt.Errorf("revoke access-policy session credentials: %w", err)
	}

	if _, err := tx.Exec(
		ctx,
		`UPDATE sessions
		SET updated_at = GREATEST(updated_at, ?), revoked_at = GREATEST(created_at, ?),
			revocation_reason = ?
		WHERE archived_at IS NULL
			AND revoked_at IS NULL
			AND expires_at > ?
			AND idle_expires_at > ?
			AND (
				(? AND authentication_method = 'password' AND authentication_provider_id = '')
				OR authentication_provider_id = ANY(?)
			)`,
		at,
		at,
		string(model.SessionRevocationAccessPolicyChanged),
		at,
		at,
		localDisabled,
		providerArray,
	); err != nil {
		return nil, fmt.Errorf("revoke access-policy sessions: %w", err)
	}

	byUser := make(map[string]*store.AccessPolicySessionRevocation)
	userOrder := make([]string, 0)
	for _, row := range sessionRows {
		entry := byUser[row.UserID]
		if entry == nil {
			userID, err := model.ParseUserID(row.UserID)
			if err != nil {
				return nil, invalidPersistedState("session", "user_id", err)
			}

			entry = &store.AccessPolicySessionRevocation{UserID: userID, SessionIDs: []model.SessionID{}, AccessTokenHashes: []string{}}
			byUser[row.UserID], userOrder = entry, append(userOrder, row.UserID)
		}

		sessionID, err := model.ParseSessionID(row.SessionID)
		if err != nil {
			return nil, invalidPersistedState("session", "id", err)
		}

		entry.SessionIDs = append(entry.SessionIDs, sessionID)
	}

	for _, row := range credentialRows {
		if entry := byUser[row.UserID]; entry != nil {
			entry.AccessTokenHashes = append(entry.AccessTokenHashes, row.TokenHash)
		}
	}

	result := make([]store.AccessPolicySessionRevocation, 0, len(userOrder))
	for _, userID := range userOrder {
		result = append(result, *byUser[userID])
	}

	return result, nil
}

// revokeRoleHolderSingleFactorSessions revokes, with their credentials, the live
// Sessions of role holders that have not proved a second factor. Replacement
// calls it when the policy starts requiring two-step verification of role holders
// and the administrator chose revoke_existing_sessions.
func revokeRoleHolderSingleFactorSessions(
	ctx context.Context,
	tx *sqlxTxWrapper,
	at time.Time,
) ([]store.AccessPolicySessionRevocation, error) {
	sessionRows := []struct {
		UserID    string `db:"user_id"`
		SessionID string `db:"session_id"`
	}{}
	if err := tx.Select(ctx, &sessionRows, `SELECT s.user_id, s.id AS session_id
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE `+roleHolderSingleFactorSessionPredicate+` AND `+roleHolderUserPredicate+`
		ORDER BY s.user_id, s.id
		FOR UPDATE OF s`, at); err != nil {
		return nil, fmt.Errorf("select role holder single-factor sessions for revocation: %w", err)
	}

	if len(sessionRows) == 0 {
		return []store.AccessPolicySessionRevocation{}, nil
	}

	sessionIDs := make([]string, 0, len(sessionRows))
	for _, row := range sessionRows {
		sessionIDs = append(sessionIDs, row.SessionID)
	}

	credentialRows := []accessPolicySessionCredentialRow{}
	if err := tx.Select(ctx, &credentialRows, `SELECT s.user_id, s.id AS session_id, c.token_hash
		FROM sessions s
		JOIN session_credentials c ON c.session_id = s.id
		WHERE s.id = ANY(?)
			AND c.kind = 'access'
			AND c.archived_at IS NULL
			AND c.revoked_at IS NULL
		ORDER BY s.user_id, s.id`, pq.Array(sessionIDs)); err != nil {
		return nil, fmt.Errorf("select role holder single-factor session credentials for revocation: %w", err)
	}

	// As in revokeSessionsForDisabledAccessMethods, a leading node clock must
	// not produce a revocation that precedes the row's creation.
	if _, err := tx.Exec(ctx, `UPDATE session_credentials
		SET updated_at = GREATEST(updated_at, ?), revoked_at = GREATEST(created_at, ?)
		WHERE session_id = ANY(?)
			AND archived_at IS NULL
			AND revoked_at IS NULL`, at, at, pq.Array(sessionIDs)); err != nil {
		return nil, fmt.Errorf("revoke role holder single-factor session credentials: %w", err)
	}

	if _, err := tx.Exec(ctx, `UPDATE sessions
		SET updated_at = GREATEST(updated_at, ?), revoked_at = GREATEST(created_at, ?),
			revocation_reason = ?
		WHERE id = ANY(?)
			AND revoked_at IS NULL`, at, at, string(model.SessionRevocationAccessPolicyChanged), pq.Array(sessionIDs)); err != nil {
		return nil, fmt.Errorf("revoke role holder single-factor sessions: %w", err)
	}

	revocations := []store.AccessPolicySessionRevocation{}
	byUser := map[string]int{}
	for _, row := range sessionRows {
		index, seen := byUser[row.UserID]
		if !seen {
			userID, err := model.ParseUserID(row.UserID)
			if err != nil {
				return nil, invalidPersistedState("session", "user_id", err)
			}

			index = len(revocations)
			byUser[row.UserID] = index
			revocations = append(revocations, store.AccessPolicySessionRevocation{
				UserID:            userID,
				SessionIDs:        []model.SessionID{},
				AccessTokenHashes: []string{},
			})
		}

		sessionID, err := model.ParseSessionID(row.SessionID)
		if err != nil {
			return nil, invalidPersistedState("session", "id", err)
		}

		revocations[index].SessionIDs = append(revocations[index].SessionIDs, sessionID)
	}

	for _, row := range credentialRows {
		if index, seen := byUser[row.UserID]; seen {
			revocations[index].AccessTokenHashes = append(revocations[index].AccessTokenHashes, row.TokenHash)
		}
	}

	return revocations, nil
}

// mergeAccessPolicySessionRevocations joins two per-User revocation lists in
// User order. A Session appears in at most one list: the first revocation
// already ended it before the second selected live Sessions.
func mergeAccessPolicySessionRevocations(first, second []store.AccessPolicySessionRevocation) []store.AccessPolicySessionRevocation {
	byUser := make(map[model.UserID]int, len(first)+len(second))
	merged := make([]store.AccessPolicySessionRevocation, 0, len(first)+len(second))
	for _, list := range [][]store.AccessPolicySessionRevocation{first, second} {
		for _, revocation := range list {
			index, seen := byUser[revocation.UserID]
			if !seen {
				index = len(merged)
				byUser[revocation.UserID] = index
				merged = append(merged, store.AccessPolicySessionRevocation{
					UserID:            revocation.UserID,
					SessionIDs:        []model.SessionID{},
					AccessTokenHashes: []string{},
				})
			}

			merged[index].SessionIDs = append(merged[index].SessionIDs, revocation.SessionIDs...)
			merged[index].AccessTokenHashes = append(merged[index].AccessTokenHashes, revocation.AccessTokenHashes...)
		}
	}

	sort.Slice(merged, func(i, j int) bool { return merged[i].UserID < merged[j].UserID })
	return merged
}

// shortenLiveSessionDeadlines pulls the deadlines of every live Session in to
// a shorter effective Session length, in the replacement transaction: idle
// expiry to last activity plus the new idle length, absolute expiry to
// creation plus the new absolute length, and each credential to its Session's
// new absolute expiry. LEAST never extends a running Session, so a longer
// length leaves it unchanged.
func shortenLiveSessionDeadlines(
	ctx context.Context,
	tx *sqlxTxWrapper,
	current, candidate *model.AccessPolicy,
	limits model.SessionLengthLimits,
	at time.Time,
) ([]store.AccessPolicySessionDeadlineChange, error) {
	currentIdle, currentAbsolute := current.Settings().EffectiveSessionMinutes(limits)
	idle, absolute := candidate.Settings().EffectiveSessionMinutes(limits)
	if idle >= currentIdle && absolute >= currentAbsolute {
		return []store.AccessPolicySessionDeadlineChange{}, nil
	}

	rows := []struct {
		UserID    string `db:"user_id"`
		SessionID string `db:"session_id"`
	}{}
	// A Session older than the new absolute length ends one microsecond after
	// its last activity, already in the past. The new deadlines therefore
	// stay after last activity and creation, the idle deadline never passes
	// the absolute one, and neither ever moves later, so every row keeps the
	// Session invariants.
	if err := tx.Select(ctx, &rows, `WITH shortened AS (
			SELECT id,
				GREATEST(
					LEAST(expires_at, created_at + make_interval(mins => $2)),
					last_activity_at + interval '1 microsecond'
				) AS expires_at,
				last_activity_at + make_interval(mins => $3) AS idle_at
			FROM sessions
			WHERE archived_at IS NULL
				AND revoked_at IS NULL
				AND expires_at > $1
				AND idle_expires_at > $1
			FOR UPDATE
		)
		UPDATE sessions s
		SET updated_at = GREATEST(s.updated_at, $1), expires_at = shortened.expires_at,
			idle_expires_at = LEAST(s.idle_expires_at, shortened.idle_at, shortened.expires_at)
		FROM shortened
		WHERE s.id = shortened.id
			AND (
				shortened.expires_at < s.expires_at
				OR LEAST(shortened.idle_at, shortened.expires_at) < s.idle_expires_at
			)
		RETURNING s.user_id, s.id AS session_id`, at, absolute, idle); err != nil {
		return nil, fmt.Errorf("shorten live session deadlines: %w", err)
	}

	if len(rows) == 0 {
		return []store.AccessPolicySessionDeadlineChange{}, nil
	}

	sessionIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		sessionIDs = append(sessionIDs, row.SessionID)
	}

	// A credential keeps expiring after its creation and any use, so one
	// issued or used after the new absolute expiry ends just after that; its
	// Session has already ended.
	if _, err := tx.Exec(ctx, `UPDATE session_credentials c
		SET updated_at = GREATEST(c.updated_at, ?),
			expires_at = GREATEST(
				s.expires_at, c.created_at + interval '1 microsecond',
				COALESCE(
					c.used_at + interval '1 microsecond', c.created_at + interval '1 microsecond'
				)
			)
		FROM sessions s
		WHERE s.id = c.session_id
			AND s.id = ANY(?)
			AND c.archived_at IS NULL
			AND c.revoked_at IS NULL
			AND c.expires_at > s.expires_at`, at, pq.Array(sessionIDs)); err != nil {
		return nil, fmt.Errorf("cap shortened session credentials: %w", err)
	}

	credentialRows := []accessPolicySessionCredentialRow{}
	if err := tx.Select(ctx, &credentialRows, `SELECT s.user_id, s.id AS session_id, c.token_hash
		FROM sessions s
		JOIN session_credentials c ON c.session_id = s.id
		WHERE s.id = ANY(?)
			AND c.kind = 'access'
			AND c.archived_at IS NULL
			AND c.revoked_at IS NULL
		ORDER BY s.user_id, s.id`, pq.Array(sessionIDs)); err != nil {
		return nil, fmt.Errorf("select shortened session credentials: %w", err)
	}

	changes := []store.AccessPolicySessionDeadlineChange{}
	byUser := map[string]int{}
	for _, row := range rows {
		index, seen := byUser[row.UserID]
		if !seen {
			userID, err := model.ParseUserID(row.UserID)
			if err != nil {
				return nil, invalidPersistedState("session", "user_id", err)
			}

			index = len(changes)
			byUser[row.UserID] = index
			changes = append(changes, store.AccessPolicySessionDeadlineChange{
				UserID:            userID,
				SessionIDs:        []model.SessionID{},
				AccessTokenHashes: []string{},
			})
		}

		sessionID, err := model.ParseSessionID(row.SessionID)
		if err != nil {
			return nil, invalidPersistedState("session", "id", err)
		}

		changes[index].SessionIDs = append(changes[index].SessionIDs, sessionID)
	}

	for _, row := range credentialRows {
		if index, seen := byUser[row.UserID]; seen {
			changes[index].AccessTokenHashes = append(changes[index].AccessTokenHashes, row.TokenHash)
		}
	}

	sort.Slice(changes, func(i, j int) bool { return changes[i].UserID < changes[j].UserID })
	return changes, nil
}

// nullableMinutes stores an unset Session length as NULL.
func nullableMinutes(minutes int) sql.NullInt64 {
	return sql.NullInt64{Int64: int64(minutes), Valid: minutes > 0}
}

func accessPolicyRevokedSessionCount(revocations []store.AccessPolicySessionRevocation) int {
	total := 0
	for _, revocation := range revocations {
		total += len(revocation.SessionIDs)
	}
	return total
}

func getAccessPolicy(ctx context.Context, executor sqlxExecutor, lock string) (*model.AccessPolicy, error) {
	var row accessPolicyRow
	query := `SELECT id, revision, created_at, updated_at, local_login_enabled,
			public_registration_enabled, invitation_admission_enabled,
			invitation_local_credential_enabled, desktop_authorization_enabled, provider_admissions,
			role_holder_mfa_required, session_idle_minutes, session_absolute_minutes
		FROM access_policies
		WHERE singleton = 1`
	if lock != "" {
		query += " " + lock
	}
	if err := executor.Get(ctx, &row, query); err != nil {
		return nil, translateError("access_policy", "singleton", err)
	}
	return row.model()
}

func (row accessPolicyRow) model() (*model.AccessPolicy, error) {
	id, err := model.ParseAccessPolicyID(row.ID)
	if err != nil {
		return nil, invalidPersistedState("access_policy", "id", err)
	}

	providers := map[string]model.ProviderAdmissionMode{}
	if err := json.Unmarshal(row.ProviderAdmissions, &providers); err != nil {
		return nil, invalidPersistedState("access_policy", "provider_admissions", err)
	}

	policy := &model.AccessPolicy{
		ID:                               id,
		Revision:                         row.Revision,
		CreatedAt:                        model.TimeUTC(row.CreatedAt),
		UpdatedAt:                        model.TimeUTC(row.UpdatedAt),
		LocalLoginEnabled:                row.LocalLoginEnabled,
		PublicRegistrationEnabled:        row.PublicRegistrationEnabled,
		InvitationAdmissionEnabled:       row.InvitationAdmissionEnabled,
		InvitationLocalCredentialEnabled: row.InvitationLocalCredentialEnabled,
		DesktopAuthorizationEnabled:      row.DesktopAuthorizationEnabled,
		ProviderAdmissions:               providers,
		RoleHolderMFARequired:            row.RoleHolderMFARequired,
		SessionIdleMinutes:               int(row.SessionIdleMinutes.Int64),
		SessionAbsoluteMinutes:           int(row.SessionAbsoluteMinutes.Int64),
	}
	if err := validatePersistedModel("access_policy", policy); err != nil {
		return nil, err
	}

	return policy, nil
}

func listAccessPolicyHistory(
	ctx context.Context,
	executor sqlxExecutor,
	policyID model.AccessPolicyID,
	limit int,
) ([]*model.AccessPolicyTransition, error) {
	if limit == 0 {
		return []*model.AccessPolicyTransition{}, nil
	}

	rows := []accessPolicyTransitionRow{}
	if err := executor.Select(
		ctx,
		&rows,
		`SELECT access_policy_id, from_revision, to_revision, actor_user_id, changed_fields,
			changed_at, outcome
		FROM access_policy_transitions
		WHERE access_policy_id = ?
		ORDER BY to_revision DESC
		LIMIT ?`,
		policyID.String(),
		limit,
	); err != nil {
		return nil, fmt.Errorf("list access policy transitions: %w", err)
	}

	result := make([]*model.AccessPolicyTransition, 0, len(rows))
	for _, row := range rows {
		transition, err := row.model()
		if err != nil {
			return nil, err
		}

		result = append(result, transition)
	}

	return result, nil
}

func (row accessPolicyTransitionRow) model() (*model.AccessPolicyTransition, error) {
	policyID, err := model.ParseAccessPolicyID(row.PolicyID)
	if err != nil {
		return nil, invalidPersistedState("access_policy_transition", "access_policy_id", err)
	}

	actorID, err := model.ParseUserID(row.ActorID)
	if err != nil {
		return nil, invalidPersistedState("access_policy_transition", "actor_user_id", err)
	}

	transition := &model.AccessPolicyTransition{
		PolicyID:      policyID,
		FromRevision:  row.FromRevision,
		ToRevision:    row.ToRevision,
		ActorID:       actorID,
		ChangedFields: append([]string(nil), row.ChangedFields...),
		ChangedAt:     model.TimeUTC(row.ChangedAt),
		Outcome:       model.AccessPolicyTransitionOutcome(row.Outcome),
	}
	if err := transition.Validate(); err != nil {
		return nil, invalidPersistedState("access_policy_transition", "value", err)
	}

	return transition, nil
}

func insertAccessPolicyTransition(ctx context.Context, executor sqlxExecutor, transition *model.AccessPolicyTransition) error {
	if transition == nil || transition.Validate() != nil {
		return store.NewErrInvalidInput("access_policy_transition", "value", nil)
	}

	_, err := executor.Exec(
		ctx,
		`INSERT INTO access_policy_transitions (
			access_policy_id, from_revision, to_revision, actor_user_id, changed_fields, changed_at,
			outcome
		)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		transition.PolicyID.String(),
		transition.FromRevision,
		transition.ToRevision,
		transition.ActorID.String(),
		pq.Array(transition.ChangedFields),
		transition.ChangedAt,
		string(transition.Outcome),
	)
	if err != nil {
		return fmt.Errorf("save access policy transition: %w", translateError("access_policy_transition", transition.PolicyID.String(), err))
	}

	return nil
}

func validateAccessPolicyPreflight(input *store.AccessPolicyPreflight) error {
	if input == nil ||
		input.ExpectedRevision < 1 ||
		input.CheckedAt.IsZero() ||
		input.Settings.Validate() != nil ||
		input.Settings.ValidateSessionLengths(input.SessionLimits) != nil ||
		!validAccessDeploymentCapabilities(input.Capabilities) {
		return store.NewErrInvalidInput("access_policy", "preflight", nil)
	}

	return nil
}

func validateAccessPolicyReplacement(input *store.AccessPolicyReplacement, command *store.CommandIdempotency) error {
	if input == nil ||
		!input.ActorID.IsValid() ||
		!input.AuditEventID.IsValid() ||
		input.AuditAt.IsZero() ||
		command == nil ||
		command.UserID != input.ActorID ||
		command.Operation != "access_policy.replace" ||
		command.Retention <= 0 ||
		command.Wait <= 0 {
		return store.NewErrInvalidInput("access_policy", "replacement", nil)
	}

	return validateAccessPolicyPreflight(&input.Preflight)
}

func accessPolicyBlockers(
	ctx context.Context,
	executor sqlxExecutor,
	current *model.AccessPolicy,
	settings model.AccessPolicySettings,
	capabilities store.AccessDeploymentCapabilities,
	at time.Time,
) ([]store.AccessPolicyBlocker, error) {
	blockers := make([]store.AccessPolicyBlocker, 0)
	for id, mode := range settings.ProviderAdmissions {
		capability, available := capabilities.Providers[id]
		if !available {
			blockers = append(blockers, store.AccessPolicyBlocker{Code: store.AccessPolicyBlockerProviderUnavailable, ProviderID: id})
			continue
		}

		if mode == model.ProviderAdmissionAutoProvision && !capability.AutoProvision {
			blockers = append(blockers, store.AccessPolicyBlocker{Code: store.AccessPolicyBlockerProviderAdmissionUnsupported, ProviderID: id})
		}

		if mode == model.ProviderAdmissionInvitationRequired &&
			current.ProviderAdmissions[id] != model.ProviderAdmissionInvitationRequired &&
			!capabilities.DurableMail {
			blockers = append(blockers, store.AccessPolicyBlocker{Code: store.AccessPolicyBlockerInvitationMailUnavailable, ProviderID: id})
		}
	}

	if settings.InvitationAdmissionEnabled && !current.InvitationAdmissionEnabled && !capabilities.DurableMail {
		blockers = append(blockers, store.AccessPolicyBlocker{Code: store.AccessPolicyBlockerInvitationMailUnavailable})
	}

	// Requiring two-step verification of role holders while this server cannot enroll
	// a second factor would leave every role holder restricted.
	if settings.RoleHolderMFARequired && !current.RoleHolderMFARequired && !capabilities.MFA {
		blockers = append(blockers, store.AccessPolicyBlocker{Code: store.AccessPolicyBlockerMFAUnavailable})
	}

	administratorHasPath, err := hasUsableSystemAdministratorAuthenticationPath(
		ctx, executor, settings, capabilities, at, systemAdministratorAuthenticationPathScope{},
	)
	if err != nil {
		return nil, err
	}
	if !administratorHasPath {
		blockers = append(blockers, store.AccessPolicyBlocker{Code: store.AccessPolicyBlockerLastAdministratorPath})
	}

	sort.Slice(blockers, func(i, j int) bool {
		if blockers[i].Code == blockers[j].Code {
			return blockers[i].ProviderID < blockers[j].ProviderID
		}
		return blockers[i].Code < blockers[j].Code
	})
	return blockers, nil
}

var _ store.AccessPolicyStore = (*SQLAccessPolicyStore)(nil)
