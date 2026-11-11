// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/channels/api4/user.go login/logout handlers,
// its session-authenticated boundary, and browser session-cookie behavior.
// Proctor keeps explicit catalog classification and an immutable request
// principal without Mattermost's product-specific handler context.

package route

import (
	"context"
	"net/http"
	"strings"

	"github.com/sudosylabs/proctor/server/model"
)

type principalContextKey struct{}
type credentialContextKey struct{}
type credentialSourceContextKey struct{}

// CredentialSource says how a request presented its credential. It decides
// whether CSRF verification applies and whether an operation rewrites cookies.
type CredentialSource string

// CredentialBearer is an Authorization: Bearer header, CredentialCookie a
// browser cookie, and CredentialDPoP an Authorization: DPoP header bound to a
// proof.
const (
	CredentialBearer CredentialSource = "bearer"
	CredentialCookie CredentialSource = "cookie"
	CredentialDPoP   CredentialSource = "dpop"
)

// Credential is a credential the kernel extracted from a request without
// redeeming it.
type Credential struct {
	Token  string
	Source CredentialSource
}

func requestAccessCredential(request *http.Request) (Credential, error) {
	return requestCredentialFrom(request, BrowserAccessCookieName)
}

func requestRefreshCredential(request *http.Request) (Credential, error) {
	return requestCredentialFrom(request, BrowserRefreshCookieName)
}

func requestCredentialFrom(
	request *http.Request,
	cookieName string,
) (Credential, error) {
	token, source, hasAuthorization, appErr := optionalAuthorizationCredential(request)
	if appErr != nil {
		return Credential{}, appErr
	}
	cookie, appErr := CookieValue(request, cookieName)
	if appErr != nil {
		return Credential{}, appErr
	}
	if hasAuthorization && cookie != "" {
		return Credential{}, ambiguousCredentialError()
	}
	if hasAuthorization {
		return Credential{Token: token, Source: source}, nil
	}
	if cookie != "" {
		return Credential{Token: cookie, Source: CredentialCookie}, nil
	}
	return Credential{}, AuthenticationRequired()
}

func optionalAuthorizationCredential(request *http.Request) (string, CredentialSource, bool, error) {
	values := request.Header.Values("Authorization")
	if len(values) == 0 {
		return "", "", false, nil
	}
	if len(values) != 1 {
		return "", "", false, AuthenticationRequired()
	}
	parts := strings.Fields(values[0])
	if len(parts) != 2 || parts[1] == "" {
		return "", "", false, AuthenticationRequired()
	}
	switch {
	case strings.EqualFold(parts[0], "Bearer"):
		return parts[1], CredentialBearer, true, nil
	case parts[0] == "DPoP":
		return parts[1], CredentialDPoP, true, nil
	default:
		return "", "", false, AuthenticationRequired()
	}
}

// CookieValue returns the named cookie's value, or "" when the request does not
// carry it. A repeated or empty cookie is authentication.credential_ambiguous.
func CookieValue(
	request *http.Request,
	name string,
) (string, error) {
	var value string
	for _, cookie := range request.Cookies() {
		if cookie.Name != name {
			continue
		}
		if value != "" || cookie.Value == "" {
			return "", ambiguousCredentialError()
		}
		value = cookie.Value
	}
	return value, nil
}

// Principal returns the Principal the kernel authenticated for the request. It
// reports false on a public route, and for a Principal whose MFA recovery
// restriction is inconsistent.
func Principal(ctx context.Context) (model.Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(model.Principal)
	return principal, ok && principal.ValidateMFARecovery() == nil
}

// RefreshCredential returns the refresh credential of a request to an
// AuthRefreshCredentialRequired route. The kernel only extracted it, and
// verified CSRF when it came from a cookie; the operation redeems it.
func RefreshCredential(ctx context.Context) (Credential, bool) {
	credential, ok := ctx.Value(credentialContextKey{}).(Credential)
	return credential, ok && credential.Token != "" && credential.Source != ""
}

// CredentialSourceOf returns how the request presented the credential the
// kernel admitted, or "" on a public route.
func CredentialSourceOf(ctx context.Context) CredentialSource {
	source, _ := ctx.Value(credentialSourceContextKey{}).(CredentialSource)
	return source
}
