// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/channels/app/authentication.go,
// server/channels/app/login.go, and server/channels/app/session.go. Proctor
// keeps the single application authentication flow, generic login failures,
// server-side revocable sessions, activity debouncing, and boundary-safe
// errors while using split hashed access/refresh credentials and rotation.

package identity

import (
	"context"
	"errors"
	"strings"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type localProof struct {
	AuthenticationGeneration int64
	MFARecoveryRequired      bool
	PasswordProof            store.PasswordCredentialProof
	User                     *model.User
	AuthenticationStrength   model.AuthenticationStrength
	AuthenticatedAt          int64
	MFACompletedAt           int64
	receipt                  authenticationAttemptReceipt
	failures                 authenticationFailureSubject
}

// localSubject records how far a failed local sign-in got: past the rate
// limit, and which account it targeted when known.
type localSubject struct {
	admitted bool
	userID   model.UserID
}

// authenticateLocal only proves the identity; the caller decides which
// credential or session consumes the proof.
func (s *Service) authenticateLocal(
	ctx context.Context,
	command LoginCommand,
) (*localProof, error) {
	proof, _, err := s.proveLocal(ctx, command)
	return proof, err
}

func (s *Service) proveLocal(
	ctx context.Context,
	command LoginCommand,
) (*localProof, localSubject, error) {
	var subject localSubject
	proof, err := s.proveLocalSubject(ctx, command, &subject)
	return proof, subject, err
}

func (s *Service) proveLocalSubject(
	ctx context.Context,
	command LoginCommand,
	subject *localSubject,
) (*localProof, error) {
	receipt, err := s.checkLoginRateLimit(ctx, command.LoginID, command.Source)
	if err != nil {
		return nil, err
	}

	subject.admitted = true
	if err = s.admitLocalLogin(ctx, command); err != nil {
		return nil, err
	}

	account, err := s.findLoginUser(ctx, command.LoginID)
	if err == nil && account != nil {
		subject.userID = account.ID
	}

	if err != nil && !store.IsNotFound(err) {
		if dummyErr := s.hasher.VerifyDummy(ctx, command.Password); dummyErr != nil {
			return nil, passwordWorkError(dummyErr, "authentication.internal")
		}
		return nil, authenticationUnavailable(err)
	}

	if err != nil {
		account = nil
	}

	failures, err := s.delayAfterLoginFailures(ctx, command.LoginID, account)
	if err != nil {
		return nil, err
	}

	if account == nil {
		if dummyErr := s.hasher.VerifyDummy(ctx, command.Password); dummyErr != nil {
			return nil, passwordWorkError(dummyErr, "authentication.internal")
		}
		return nil, s.recordLoginFailure(ctx, failures)
	}

	credential, err := s.passwords.GetByUser(ctx, account.ID)
	if err != nil {
		if dummyErr := s.hasher.VerifyDummy(ctx, command.Password); dummyErr != nil {
			return nil, passwordWorkError(dummyErr, "authentication.internal")
		}
		if !store.IsNotFound(err) {
			return nil, authenticationUnavailable(err)
		}

		return nil, s.recordLoginFailure(ctx, failures)
	}

	// Do the same password work for a disabled account so timing doesn't
	// reveal it. Its MFA state is not consulted: recovery state exists only
	// for active users.
	if !account.IsActive() {
		if dummyErr := s.hasher.VerifyDummy(ctx, command.Password); dummyErr != nil {
			return nil, passwordWorkError(dummyErr, "authentication.internal")
		}
		return nil, s.recordLoginFailure(ctx, failures)
	}

	recovery, err := s.mfaRecoveryState(ctx, account.ID)
	if err != nil {
		return nil, err
	}

	if verifyErr := s.hasher.Verify(ctx, credential.PasswordHash, command.Password); verifyErr != nil {
		if !errors.Is(verifyErr, ErrPasswordMismatch) {
			return nil, passwordWorkError(verifyErr, "authentication.internal")
		}
		return nil, s.recordLoginFailure(ctx, failures)
	}

	if !account.IsActive() {
		return nil, invalidCredentialsAppError()
	}

	// Prepare any hash upgrade before consuming a one-time second factor.
	// The guarded credential write still follows successful MFA below.
	var rehashed string
	if s.hasher.NeedsRehash(credential.PasswordHash) {
		var hashErr error
		rehashed, hashErr = s.hasher.Hash(ctx, command.Password)
		if hashErr != nil {
			return nil, passwordWorkError(hashErr, "authentication.internal")
		}
	}

	now := s.now()
	authenticationStrength, mfaCompletedAt, err := s.verifyLoginMFA(
		ctx, account.ID.String(), command.MFACode, now,
	)
	if err != nil {
		return nil, err
	}
	if err = s.applyLocalRehash(ctx, credential, rehashed); err != nil {
		return nil, err
	}

	return &localProof{
		User:                     account,
		AuthenticationStrength:   authenticationStrength,
		AuthenticationGeneration: recovery.Generation,
		MFARecoveryRequired:      recovery.ReenrollmentRequired,
		PasswordProof:            store.PasswordCredentialProof{ID: credential.ID, Revision: credential.Revision},
		AuthenticatedAt:          now.UnixMilli(),
		MFACompletedAt:           mfaCompletedAt,
		receipt:                  receipt,
		failures:                 failures,
	}, nil
}

// admitLocalLogin does dummy password work before refusing, so a refused
// login takes as long as a wrong password.
func (s *Service) admitLocalLogin(ctx context.Context, command LoginCommand) error {
	localLoginAllowed, err := s.accessPolicy.AllowsLocalLogin(ctx)
	if err != nil {
		return authenticationUnavailable(err)
	}
	if !localLoginAllowed {
		if dummyErr := s.hasher.VerifyDummy(ctx, command.Password); dummyErr != nil {
			return passwordWorkError(dummyErr, "authentication.internal")
		}
		return invalidCredentialsAppError()
	}
	if command.LoginID == "" ||
		len(command.LoginID) > model.UserEmailMaxLength ||
		len(command.Password) > s.hasher.maximumPasswordLength() {
		if dummyErr := s.hasher.VerifyDummy(ctx, "invalid-password-length"); dummyErr != nil {
			return passwordWorkError(dummyErr, "authentication.internal")
		}
		return invalidCredentialsAppError()
	}
	return nil
}

func (s *Service) findLoginUser(ctx context.Context, loginID string) (*model.User, error) {
	loginID = strings.ToLower(strings.TrimSpace(loginID))
	if strings.Contains(loginID, "@") {
		return s.users.GetByEmail(ctx, loginID)
	}
	return s.users.GetByUsername(ctx, loginID)
}

func (s *Service) applyLocalRehash(ctx context.Context, credential *model.PasswordCredential, rehashed string) error {
	if rehashed != "" {
		if rehashErr := s.passwords.Rehash(ctx, &store.PasswordCredentialRehash{
			ID:               credential.ID,
			UserID:           credential.UserID,
			ExpectedHash:     credential.PasswordHash,
			ExpectedRevision: credential.Revision,
			PasswordHash:     rehashed,
		}); rehashErr != nil {
			if errors.Is(rehashErr, store.ErrPasswordCredentialChanged) {
				return invalidCredentialsAppError()
			}
			return authenticationUnavailable(rehashErr)
		}
	}

	return nil
}
