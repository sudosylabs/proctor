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
	"fmt"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// liveDesktopSessionCondition selects the exact live Desktop Session bound to
// a reauthentication, its active User and registered key. The caller supplies
// the transition instant twice, followed by the access credential identifier
// when one is required.
const liveDesktopSessionCondition = `sessions.client_type='desktop'
	   AND sessions.archived_at IS NULL AND sessions.revoked_at IS NULL
	   AND sessions.expires_at>? AND sessions.idle_expires_at>?
	   AND EXISTS (SELECT 1 FROM users WHERE id=sessions.user_id AND archived_at IS NULL AND disabled_at IS NULL)
	   AND EXISTS (SELECT 1 FROM desktop_registrations registration
	                WHERE registration.id=sessions.desktop_registration_id AND registration.user_id=sessions.user_id
	                  AND registration.key_thumbprint=sessions.dpop_key_thumbprint AND registration.revoked_at IS NULL)`

// liveAccessCredentialCondition requires the Desktop Session's exact current
// access credential. The caller supplies the credential and the instant.
const liveAccessCredentialCondition = `EXISTS (SELECT 1 FROM session_credentials credential
	    WHERE credential.id=? AND credential.session_id=sessions.id AND credential.kind='access'
	      AND credential.archived_at IS NULL AND credential.revoked_at IS NULL AND credential.expires_at>?)`

type desktopReauthenticationRow struct {
	ID                           string         `db:"id"`
	CreatedAt                    time.Time      `db:"created_at"`
	ExpiresAt                    time.Time      `db:"expires_at"`
	State                        string         `db:"state"`
	UserID                       string         `db:"user_id"`
	SessionID                    string         `db:"session_id"`
	ExpectedAuthenticationMethod string         `db:"expected_authentication_method"`
	ExpectedProviderID           sql.NullString `db:"expected_provider_id"`
	AuthenticationMethod         sql.NullString `db:"authentication_method"`
	AuthenticationProviderID     sql.NullString `db:"authentication_provider_id"`
	ExternalIdentityID           sql.NullString `db:"external_identity_id"`
	PasswordCredentialID         sql.NullString `db:"password_credential_id"`
	PasswordCredentialRevision   sql.NullInt64  `db:"password_credential_revision"`
	AuthenticatedAt              sql.NullTime   `db:"authenticated_at"`
}

const desktopReauthenticationColumns = `id, created_at, expires_at, state, user_id, session_id,
	expected_authentication_method, expected_provider_id, authentication_method, authentication_provider_id,
	external_identity_id, password_credential_id, password_credential_revision, authenticated_at`

// CreateDesktopReauthentication starts a reauthentication handoff for the
// caller's exact live Desktop Session, pinning that Session's own
// authentication method and provider as the only acceptable proof.
func (s SQLBrowserAuthenticationStore) CreateDesktopReauthentication(ctx context.Context, input *store.DesktopReauthenticationCreation) (*store.DesktopAuthorizationCreated, error) {
	if input == nil || !input.ID.IsValid() || !input.InstitutionID.IsValid() ||
		model.ValidateBrowserAuthenticationIssuer(input.Issuer, true) != nil ||
		!model.IsValidTokenHash(input.HandleHash) || !model.IsValidTokenHash(input.BrowserProofHash) ||
		input.HandleHash == input.BrowserProofHash || !model.IsValidTokenHash(input.StateHash) ||
		model.ValidateDesktopAuthorizationCallback(input.CallbackURL) != nil ||
		!model.IsValidCredentialToken(input.CodeChallenge) || !input.UserID.IsValid() || !input.SessionID.IsValid() ||
		!input.CredentialID.IsValid() || !input.DesktopRegistrationID.IsValid() ||
		!model.IsValidDPoPKeyThumbprint(input.KeyThumbprint) ||
		input.Lifetime <= 0 || input.Lifetime > model.BrowserAuthenticationTransactionLifetime {
		return nil, store.NewErrInvalidInput("browser_authentication_transaction", "desktop_reauthentication", nil)
	}
	return runSQLTransaction(ctx, s.GetMaster().Begin, "create desktop reauthentication", func(ctx context.Context, tx *sqlxTxWrapper) (*store.DesktopAuthorizationCreated, error) {
		if err := lockUserSessions(ctx, tx, input.UserID.String()); err != nil {
			return nil, err
		}
		var at time.Time
		if err := tx.Get(ctx, &at, `SELECT clock_timestamp()`); err != nil {
			return nil, fmt.Errorf("read desktop reauthentication creation time: %w", err)
		}
		at = model.TimeUTC(at)
		session, err := getLiveDesktopReauthenticationSession(ctx, tx, input.SessionID, input.UserID,
			input.DesktopRegistrationID, input.KeyThumbprint, input.CredentialID, at)
		if err != nil {
			return nil, err
		}
		if err = requireCurrentAuthenticationMethod(ctx, tx, session.AuthenticationMethod, session.AuthenticationProviderID); err != nil {
			return nil, err
		}
		expiresAt := at.Add(input.Lifetime)
		if _, err = tx.Exec(ctx, `INSERT INTO browser_authentication_transactions (
			id, created_at, updated_at, purpose, state, institution_id, issuer, handle_hash, browser_proof_hash,
			state_hash, callback_url, code_challenge, expected_authentication_method, expected_provider_id,
			client_type, expires_at, user_id, session_id, desktop_registration_id, dpop_key_thumbprint
		) VALUES (?, ?, ?, 'desktop_reauthentication', 'pending', ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), 'desktop', ?, ?, ?, ?, ?)`,
			input.ID.String(), at, at, input.InstitutionID.String(), strings.TrimSuffix(input.Issuer, "/"),
			input.HandleHash, input.BrowserProofHash, input.StateHash, input.CallbackURL, input.CodeChallenge,
			session.AuthenticationMethod, session.AuthenticationProviderID, expiresAt, input.UserID.String(),
			input.SessionID.String(), input.DesktopRegistrationID.String(), input.KeyThumbprint); err != nil {
			return nil, translateError("browser_authentication_transaction", input.ID.String(), err)
		}
		return &store.DesktopAuthorizationCreated{ID: input.ID, ExpiresAt: expiresAt}, nil
	})
}

// getLiveDesktopReauthenticationSession reads and locks the exact live Desktop
// Session a reauthentication refers to. A zero credential skips the access
// credential check for steps that run in the browser rather than from Desktop.
func getLiveDesktopReauthenticationSession(ctx context.Context, tx *sqlxTxWrapper, sessionID model.SessionID, userID model.UserID,
	registrationID model.DesktopRegistrationID, thumbprint string, credentialID model.SessionCredentialID, at time.Time,
) (*model.Session, error) {
	query := `SELECT ` + strings.Join(sessionSliceColumns(), ",") + ` FROM sessions
	 WHERE sessions.id=? AND sessions.user_id=? AND sessions.desktop_registration_id=? AND sessions.dpop_key_thumbprint=?
	   AND ` + liveDesktopSessionCondition
	args := []any{sessionID.String(), userID.String(), registrationID.String(), thumbprint, at, at}
	if !credentialID.IsZero() {
		query += ` AND ` + liveAccessCredentialCondition
		args = append(args, credentialID.String(), at)
	}
	var row sessionRow
	if err := tx.Get(ctx, &row, query+` FOR UPDATE OF sessions`, args...); err != nil {
		return nil, translateError("session", sessionID.String(), err)
	}
	session, err := row.model()
	if err != nil {
		return nil, err
	}
	if err = requireSessionAuthenticationGeneration(ctx, tx, session); err != nil {
		return nil, err
	}
	if session.MFARecoveryRequired {
		return nil, store.ErrMFAReenrollmentRequired
	}
	return session, nil
}

// BindDesktopReauthentication consumes the URL handle and initial fragment
// proof and replaces them with one server-owned browser binding.
func (s SQLBrowserAuthenticationStore) BindDesktopReauthentication(ctx context.Context, input *store.DesktopAuthorizationBinding) (*store.DesktopAuthorizationBound, error) {
	if input == nil || !model.IsValidTokenHash(input.HandleHash) || !model.IsValidTokenHash(input.BrowserProofHash) ||
		!model.IsValidTokenHash(input.StateHash) || !model.IsValidTokenHash(input.BindingHash) ||
		input.BindingHash == input.HandleHash || input.BindingHash == input.BrowserProofHash {
		return nil, store.NewErrInvalidInput("browser_authentication_transaction", "binding", nil)
	}
	var expiresAt time.Time
	err := s.GetMaster().Get(ctx, &expiresAt, `UPDATE browser_authentication_transactions
	   SET updated_at=bound.at, state='bound', handle_hash=NULL, browser_proof_hash=?
	  FROM (SELECT clock_timestamp() AS at) AS bound
	 WHERE purpose='desktop_reauthentication' AND state='pending' AND handle_hash=? AND browser_proof_hash=? AND state_hash=?
	   AND created_at<=bound.at AND expires_at>bound.at
	 RETURNING expires_at`, input.BindingHash, input.HandleHash, input.BrowserProofHash, input.StateHash)
	if err != nil {
		return nil, translateError("browser_authentication_transaction", "binding", err)
	}
	return &store.DesktopAuthorizationBound{ExpiresAt: expiresAt.UTC()}, nil
}

// GetDesktopReauthenticationContext returns the safe state of the live
// reauthentication selected by its browser binding.
func (s SQLBrowserAuthenticationStore) GetDesktopReauthenticationContext(ctx context.Context, bindingHash string) (*store.DesktopReauthenticationContext, error) {
	if !model.IsValidTokenHash(bindingHash) {
		return nil, store.NewErrInvalidInput("browser_authentication_transaction", "binding", nil)
	}
	var row desktopReauthenticationRow
	if err := s.GetMaster().Get(ctx, &row, `SELECT `+desktopReauthenticationColumns+`
	  FROM browser_authentication_transactions
	 WHERE purpose='desktop_reauthentication' AND state IN ('bound','authenticated') AND browser_proof_hash=?
	   AND created_at<=clock_timestamp() AND expires_at>clock_timestamp()`, bindingHash); err != nil {
		return nil, translateError("browser_authentication_transaction", "binding", err)
	}
	id, err := model.ParseBrowserAuthenticationTransactionID(row.ID)
	if err != nil {
		return nil, invalidPersistedState("browser_authentication_transaction", "id", err)
	}
	userID, err := model.ParseUserID(row.UserID)
	if err != nil {
		return nil, invalidPersistedState("browser_authentication_transaction", "user_id", err)
	}
	return &store.DesktopReauthenticationContext{ID: id, State: model.BrowserAuthenticationState(row.State), UserID: userID,
		ExpectedAuthenticationMethod: row.ExpectedAuthenticationMethod, ExpectedProviderID: row.ExpectedProviderID.String,
		CreatedAt: row.CreatedAt.UTC(), ExpiresAt: row.ExpiresAt.UTC()}, nil
}

func validateDesktopReauthenticationAuthentication(input *store.DesktopReauthenticationAuthentication) error {
	validSelector := input != nil && ((model.IsValidTokenHash(input.BindingHash) && input.TransactionID.IsZero()) ||
		(input.BindingHash == "" && input.TransactionID.IsValid()))
	if !validSelector || !input.UserID.IsValid() || input.AuthenticatedAt.IsZero() || input.AuthenticationMethod == "" {
		return store.NewErrInvalidInput("browser_authentication_transaction", "desktop_reauthentication", nil)
	}
	if input.AuthenticationMethod == "password" {
		if input.ProviderID != "" || !input.ExternalIdentityID.IsZero() || !input.PasswordProof.IsValid() {
			return store.NewErrInvalidInput("browser_authentication_transaction", "authentication_path", nil)
		}
		return nil
	}
	if !model.IsValidIdentityProviderID(input.ProviderID) || !input.ExternalIdentityID.IsValid() ||
		input.PasswordProof != (store.PasswordCredentialProof{}) {
		return store.NewErrInvalidInput("browser_authentication_transaction", "authentication_path", nil)
	}
	return nil
}

// AuthenticateDesktopReauthentication records one fresh primary proof on a
// bound reauthentication, or denies the transaction when the proof names
// another account or another identity than the Desktop Session's own.
func (s SQLBrowserAuthenticationStore) AuthenticateDesktopReauthentication(ctx context.Context, input *store.DesktopReauthenticationAuthentication) (*store.DesktopReauthenticationAuthenticationResult, error) {
	if err := validateDesktopReauthenticationAuthentication(input); err != nil {
		return nil, err
	}
	selector, selectorValue := "browser_proof_hash", input.BindingHash
	if input.TransactionID.IsValid() {
		selector, selectorValue = "id", input.TransactionID.String()
	}
	return runSQLTransaction(ctx, s.GetMaster().Begin, "authenticate desktop reauthentication", func(ctx context.Context, tx *sqlxTxWrapper) (*store.DesktopReauthenticationAuthenticationResult, error) {
		current, err := lockBoundDesktopReauthentication(ctx, tx, selector, selectorValue)
		if err != nil {
			return nil, err
		}
		if input.UserID.String() != current.UserID {
			return denyDesktopReauthentication(ctx, tx, current)
		}
		if input.AuthenticationMethod != current.ExpectedAuthenticationMethod || input.ProviderID != current.ExpectedProviderID.String {
			return nil, store.ErrAuthenticationMethodDisabled
		}
		if err = requireCurrentAuthenticationMethod(ctx, tx, input.AuthenticationMethod, input.ProviderID); err != nil {
			return nil, err
		}
		createdAt := current.CreatedAt.UTC()
		if err = requireDesktopReauthenticationGeneration(ctx, tx, input.UserID, createdAt); err != nil {
			return nil, err
		}
		var at time.Time
		if err = tx.Get(ctx, &at, `SELECT clock_timestamp()`); err != nil {
			return nil, fmt.Errorf("read desktop reauthentication time: %w", err)
		}
		at = model.TimeUTC(at)
		var session sessionRow
		if err = tx.Get(ctx, &session, `SELECT `+strings.Join(sessionSliceColumns(), ",")+` FROM sessions
			 WHERE sessions.id=? AND sessions.user_id=? AND `+liveDesktopSessionCondition+` FOR SHARE OF sessions`,
			current.SessionID, current.UserID, at, at); err != nil {
			return nil, translateError("session", current.SessionID, err)
		}
		authenticatedAt := at
		if input.AuthenticationMethod == "password" {
			if err = requireCurrentPasswordProof(ctx, tx, input.UserID, input.PasswordProof); err != nil {
				return nil, err
			}
		} else {
			// The provider must prove the Desktop Session's own identity; any
			// other identity, even of the same User, is another account.
			if session.ExternalIdentityID.String != input.ExternalIdentityID.String() {
				return denyDesktopReauthentication(ctx, tx, current)
			}
			if err = requireExactExternalIdentity(ctx, tx, input.UserID, input.ProviderID, input.ExternalIdentityID); err != nil {
				return nil, err
			}
			// Provider proof must be fresh for this handoff and is never
			// recorded later than database time.
			authenticatedAt = model.TimeUTC(input.AuthenticatedAt)
			if authenticatedAt.Before(createdAt.Truncate(time.Second)) || authenticatedAt.After(at.Add(externalReauthenticationClockSkew)) {
				return nil, store.NewErrConflict("browser_authentication_transaction", "reauthentication_time", nil)
			}
			if authenticatedAt.After(at) {
				authenticatedAt = at
			}
			if authenticatedAt.Before(createdAt) {
				authenticatedAt = createdAt
			}
		}
		result, err := tx.Exec(ctx, `UPDATE browser_authentication_transactions
		   SET updated_at=?, state='authenticated', authentication_method=?, authentication_provider_id=NULLIF(?,''),
		       external_identity_id=NULLIF(?,''), password_credential_id=NULLIF(?,''), password_credential_revision=NULLIF(?,0),
		       authenticated_at=?
		 WHERE id=? AND purpose='desktop_reauthentication' AND state='bound' AND expires_at>?`,
			at, input.AuthenticationMethod, input.ProviderID, input.ExternalIdentityID.String(),
			input.PasswordProof.ID.String(), input.PasswordProof.Revision, authenticatedAt, current.ID, at)
		if err != nil {
			return nil, translateError("browser_authentication_transaction", current.ID, err)
		}
		if err = requireOneDesktopReauthenticationRow(result, "authenticate desktop reauthentication"); err != nil {
			return nil, err
		}
		return &store.DesktopReauthenticationAuthenticationResult{}, nil
	})
}

// lockBoundDesktopReauthentication locks the Desktop Session owner's Session
// fence and then the bound reauthentication itself.
func lockBoundDesktopReauthentication(ctx context.Context, tx *sqlxTxWrapper, selector, selectorValue string) (*desktopReauthenticationRow, error) {
	const condition = ` FROM browser_authentication_transactions
	 WHERE purpose='desktop_reauthentication' AND state='bound' AND `
	const live = ` AND created_at<=clock_timestamp() AND expires_at>clock_timestamp()`
	var userID string
	if err := tx.Get(ctx, &userID, `SELECT user_id`+condition+selector+`=?`+live, selectorValue); err != nil {
		return nil, translateError("browser_authentication_transaction", "binding", err)
	}
	if err := lockUserSessions(ctx, tx, userID); err != nil {
		return nil, err
	}
	var current desktopReauthenticationRow
	if err := tx.Get(ctx, &current, `SELECT `+desktopReauthenticationColumns+condition+selector+`=?`+live+` FOR UPDATE`, selectorValue); err != nil {
		return nil, translateError("browser_authentication_transaction", "binding", err)
	}
	return &current, nil
}

// requireDesktopReauthenticationGeneration refuses a handoff started before
// the User's last MFA reset or while re-enrollment is required.
func requireDesktopReauthenticationGeneration(ctx context.Context, tx *sqlxTxWrapper, userID model.UserID, createdAt time.Time) error {
	recovery, err := getMFARecoveryState(ctx, tx, userID)
	if err != nil {
		return err
	}
	if recovery.ReenrollmentRequired {
		return store.ErrMFAReenrollmentRequired
	}
	if recovery.ResetAt.Valid && createdAt.Before(recovery.ResetAt.Time) {
		return store.ErrAuthenticationGenerationChanged
	}
	return nil
}

// denyDesktopReauthentication ends a reauthentication whose proof named
// another account and destroys every live proof.
func denyDesktopReauthentication(ctx context.Context, tx *sqlxTxWrapper, current *desktopReauthenticationRow) (*store.DesktopReauthenticationAuthenticationResult, error) {
	id := current.ID
	userID, err := model.ParseUserID(current.UserID)
	if err != nil {
		return nil, invalidPersistedState("browser_authentication_transaction", "user_id", err)
	}
	result, err := tx.Exec(ctx, `UPDATE browser_authentication_transactions
	   SET updated_at=terminal.at, state='denied', handle_hash=NULL, browser_proof_hash=NULL, state_hash=NULL,
	       callback_url=NULL, code_challenge=NULL, denied_at=terminal.at, denial_reason='account_mismatch'
	  FROM (SELECT clock_timestamp() AS at) AS terminal
	 WHERE id=? AND purpose='desktop_reauthentication' AND state='bound' AND expires_at>terminal.at`, id)
	if err != nil {
		return nil, translateError("browser_authentication_transaction", id, err)
	}
	if err = requireOneDesktopReauthenticationRow(result, "deny desktop reauthentication"); err != nil {
		return nil, err
	}
	return &store.DesktopReauthenticationAuthenticationResult{Denied: true, UserID: userID}, nil
}

func requireOneDesktopReauthenticationRow(result sql.Result, operation string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	if affected != 1 {
		return store.NewErrNotFound("browser_authentication_transaction", "binding")
	}
	return nil
}

// IssueDesktopReauthenticationCode turns an authenticated reauthentication
// into one short-lived single-use code while its Desktop Session is live.
func (s SQLBrowserAuthenticationStore) IssueDesktopReauthenticationCode(ctx context.Context, input *store.DesktopReauthenticationCodeIssue) (*store.DesktopAuthorizationCodeIssued, error) {
	if input == nil || !model.IsValidTokenHash(input.BindingHash) || !model.IsValidTokenHash(input.StateHash) ||
		!model.IsValidTokenHash(input.CodeHash) || input.CodeLifetime <= 0 || input.CodeLifetime > model.DesktopAuthorizationCodeLifetime {
		return nil, store.NewErrInvalidInput("browser_authentication_transaction", "code_issue", nil)
	}
	var row struct {
		CallbackURL   string    `db:"callback_url"`
		CodeExpiresAt time.Time `db:"code_expires_at"`
	}
	err := s.GetMaster().Get(ctx, &row, `UPDATE browser_authentication_transactions AS transaction
	   SET updated_at=terminal.at, state='code_issued', browser_proof_hash=NULL, code_hash=?,
	       code_expires_at=LEAST(terminal.at + (? * interval '1 millisecond'), transaction.expires_at)
	  FROM (SELECT clock_timestamp() AS at) AS terminal
	 WHERE transaction.purpose='desktop_reauthentication' AND transaction.state='authenticated'
	   AND transaction.browser_proof_hash=? AND transaction.state_hash=?
	   AND transaction.created_at<=terminal.at AND transaction.expires_at>terminal.at
	   AND EXISTS (SELECT 1 FROM sessions WHERE sessions.id=transaction.session_id AND sessions.user_id=transaction.user_id
	               AND sessions.desktop_registration_id=transaction.desktop_registration_id
	               AND sessions.dpop_key_thumbprint=transaction.dpop_key_thumbprint
	               AND `+strings.ReplaceAll(liveDesktopSessionCondition, "?", "terminal.at")+`)
	 RETURNING transaction.callback_url, transaction.code_expires_at`,
		input.CodeHash, input.CodeLifetime.Milliseconds(), input.BindingHash, input.StateHash)
	if err != nil {
		return nil, translateError("browser_authentication_transaction", "binding", err)
	}
	if model.ValidateDesktopAuthorizationCallback(row.CallbackURL) != nil {
		return nil, fmt.Errorf("project issued desktop reauthentication: invalid callback")
	}
	return &store.DesktopAuthorizationCodeIssued{CallbackURL: row.CallbackURL, CodeExpiresAt: row.CodeExpiresAt.UTC()}, nil
}

// CancelDesktopReauthentication ends a bound or authenticated
// reauthentication and destroys every live proof.
func (s SQLBrowserAuthenticationStore) CancelDesktopReauthentication(ctx context.Context, input *store.DesktopAuthorizationCancellation) error {
	if input == nil || !model.IsValidTokenHash(input.BindingHash) || !model.IsValidTokenHash(input.StateHash) {
		return store.NewErrInvalidInput("browser_authentication_transaction", "cancellation", nil)
	}
	result, err := s.GetMaster().Exec(ctx, `UPDATE browser_authentication_transactions
	   SET updated_at=terminal.at, state='canceled', handle_hash=NULL, browser_proof_hash=NULL, state_hash=NULL,
	       callback_url=NULL, code_challenge=NULL, authentication_method=NULL, authentication_provider_id=NULL,
	       external_identity_id=NULL, password_credential_id=NULL, password_credential_revision=NULL,
	       authenticated_at=NULL, canceled_at=terminal.at
	  FROM (SELECT clock_timestamp() AS at) AS terminal
	 WHERE purpose='desktop_reauthentication' AND browser_proof_hash=? AND state_hash=? AND state IN ('bound','authenticated')
	   AND created_at<=terminal.at AND expires_at>terminal.at`, input.BindingHash, input.StateHash)
	if err != nil {
		return translateError("browser_authentication_transaction", "binding", err)
	}
	return requireOneDesktopReauthenticationRow(result, "cancel desktop reauthentication")
}

// CompleteDesktopReauthentication consumes one code for the exact Desktop
// Session, Registration and key that started the handoff, rechecks the proof
// and refreshes that Session's primary proof with its audit in one commit.
func (s SQLBrowserAuthenticationStore) CompleteDesktopReauthentication(ctx context.Context, input *store.DesktopReauthenticationCompletion) (*store.SessionReauthenticationResult, error) {
	if input == nil || !model.IsValidTokenHash(input.CodeHash) || !model.IsValidTokenHash(input.StateHash) ||
		!model.IsValidCredentialToken(input.CodeChallenge) || !input.UserID.IsValid() || !input.SessionID.IsValid() ||
		!input.CredentialID.IsValid() || !input.DesktopRegistrationID.IsValid() ||
		!model.IsValidDPoPKeyThumbprint(input.KeyThumbprint) || !input.AuditEventID.IsValid() {
		return nil, store.NewErrInvalidInput("browser_authentication_transaction", "desktop_reauthentication_completion", nil)
	}
	return runSQLTransaction(ctx, s.GetMaster().Begin, "complete desktop reauthentication", func(ctx context.Context, tx *sqlxTxWrapper) (*store.SessionReauthenticationResult, error) {
		if err := lockUserSessions(ctx, tx, input.UserID.String()); err != nil {
			return nil, err
		}
		var at time.Time
		if err := tx.Get(ctx, &at, `SELECT clock_timestamp()`); err != nil {
			return nil, fmt.Errorf("read desktop reauthentication completion time: %w", err)
		}
		at = model.TimeUTC(at)
		var current desktopReauthenticationRow
		if err := tx.Get(ctx, &current, `SELECT `+desktopReauthenticationColumns+` FROM browser_authentication_transactions
			 WHERE purpose='desktop_reauthentication' AND state='code_issued' AND code_hash=? AND state_hash=? AND code_challenge=?
			   AND user_id=? AND session_id=? AND desktop_registration_id=? AND dpop_key_thumbprint=?
			   AND created_at<=? AND expires_at>? AND code_expires_at>? FOR UPDATE`,
			input.CodeHash, input.StateHash, input.CodeChallenge, input.UserID.String(), input.SessionID.String(),
			input.DesktopRegistrationID.String(), input.KeyThumbprint, at, at, at); err != nil {
			return nil, translateError("browser_authentication_transaction", "desktop_reauthentication_code", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE browser_authentication_transactions
			   SET updated_at=?, state='exchanged', state_hash=NULL, callback_url=NULL, code_challenge=NULL,
			       code_hash=NULL, code_expires_at=NULL, exchanged_at=?
			 WHERE id=?`, at, at, current.ID); err != nil {
			return nil, translateError("browser_authentication_transaction", current.ID, err)
		}
		if !current.AuthenticationMethod.Valid || !current.AuthenticatedAt.Valid {
			return nil, invalidPersistedState("browser_authentication_transaction", "authentication", nil)
		}
		method, providerID := current.AuthenticationMethod.String, current.AuthenticationProviderID.String
		if err := requireCurrentAuthenticationMethod(ctx, tx, method, providerID); err != nil {
			return nil, err
		}
		if err := requireDesktopReauthenticationGeneration(ctx, tx, input.UserID, current.CreatedAt.UTC()); err != nil {
			return nil, err
		}
		if method == "password" {
			proof := store.PasswordCredentialProof{ID: model.PasswordCredentialID(current.PasswordCredentialID.String),
				Revision: current.PasswordCredentialRevision.Int64}
			if err := requireCurrentPasswordProof(ctx, tx, input.UserID, proof); err != nil {
				return nil, err
			}
		} else if err := requireExactExternalIdentity(ctx, tx, input.UserID, providerID,
			model.ExternalIdentityID(current.ExternalIdentityID.String)); err != nil {
			return nil, err
		}
		session, err := getLiveDesktopReauthenticationSession(ctx, tx, input.SessionID, input.UserID,
			input.DesktopRegistrationID, input.KeyThumbprint, input.CredentialID, at)
		if err != nil {
			return nil, err
		}
		if session.AuthenticationMethod != method || session.AuthenticationProviderID != providerID ||
			session.ExternalIdentityID.String() != current.ExternalIdentityID.String {
			return nil, store.ErrAuthenticationMethodDisabled
		}
		// The proof instant never precedes the Session's recorded history.
		proofAt := transitionInstant(current.AuthenticatedAt.Time, session.AuthenticatedAt, session.ReauthenticatedAt.Time)
		return commitSessionReauthentication(ctx, tx, session, proofAt, input.AuditEventID)
	})
}
