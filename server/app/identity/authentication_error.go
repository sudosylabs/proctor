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
	"github.com/sudosylabs/proctor/server/app/apperror"
)

func invalidCredentialsAppError() error {
	return apperror.New("authentication.credentials_invalid")
}

// mfaEnrollmentRequiredAppError refuses a sign-in or refresh that cannot
// continue until the User sets up two-step verification in the browser.
func mfaEnrollmentRequiredAppError() error {
	return apperror.New("authentication.mfa.enrollment_required")
}

func authenticationUnavailable(err error) error {
	return apperror.New("authentication.internal").Wrap(err)
}

// RateLimitUnavailableAppError reports that attempt accounting failed, so the
// attempt is refused rather than admitted unbounded.
func RateLimitUnavailableAppError(err error) error {
	return apperror.New("authentication.rate_limit_unavailable").Wrap(err)
}

func appErrorCode(err error) string {
	if appErr, ok := apperror.As(err); ok {
		return appErr.Code()
	}
	return "authentication.internal"
}
