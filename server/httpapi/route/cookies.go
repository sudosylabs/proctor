// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/channels/app/login.go,
// server/channels/app/authentication.go, and server/channels/web/handlers.go.
// Proctor separates its Electron/browser cookie transport from bearer clients
// and uses a rotating signed double-submit value for CSRF protection.

package route

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
)

// The cookies and the header of the browser credential transport. The web and
// Desktop clients depend on these names.
const (
	BrowserAccessCookieName               = "PROCTOR_ACCESS"
	BrowserRefreshCookieName              = "PROCTOR_REFRESH"
	BrowserCSRFBindingCookieName          = "PROCTOR_CSRF_BINDING"
	BrowserCSRFCookieName                 = "PROCTOR_CSRF"
	BrowserExternalLoginCookieName        = "PROCTOR_EXTERNAL_LOGIN"
	BrowserInvitationProofCookieName      = "PROCTOR_INVITATION_PROOF"
	BrowserDesktopAuthorizationCookieName = "PROCTOR_DESKTOP_AUTHORIZATION"
	// BrowserDesktopReauthenticationCookieName carries the opaque binding of
	// one hosted Desktop reauthentication, scoped to its route family.
	BrowserDesktopReauthenticationCookieName = "PROCTOR_DESKTOP_REAUTHENTICATION"
	BrowserCSRFHeader                        = "X-Proctor-CSRF-Token"
)

const csrfMessage = "proctor.browser_csrf"

// Cookies writes and expires the browser credential cookies. Every cookie is
// SameSite=Lax and is Secure when the public URL is HTTPS. All are HttpOnly
// except the CSRF token, which the client must echo in BrowserCSRFHeader, and
// each binding cookie is scoped to the route family that reads it.
type Cookies struct {
	secure                      bool
	refreshPath                 string
	externalLoginPath           string
	invitationPath              string
	desktopAuthorizationPath    string
	desktopReauthenticationPath string
	now                         func() time.Time
}

// NewCookies derives the cookie attributes from the installation's public URL,
// which must be an absolute HTTP or HTTPS URL.
func NewCookies(publicURL string) (Cookies, error) {
	parsed, err := url.Parse(publicURL)
	if err != nil ||
		parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") {
		return Cookies{}, errors.New("public URL must be an absolute HTTP or HTTPS URL")
	}

	return Cookies{
		secure:                      parsed.Scheme == "https",
		refreshPath:                 model.APIURLSuffix + "/auth/refresh",
		externalLoginPath:           model.APIURLSuffix + "/auth/providers/",
		invitationPath:              model.APIURLSuffix + "/auth/browser/invitations",
		desktopAuthorizationPath:    model.APIURLSuffix + "/auth/desktop/authorizations",
		desktopReauthenticationPath: model.APIURLSuffix + "/auth/desktop/reauthentications",
		now:                         time.Now,
	}, nil
}

// AttachDesktopAuthorizationBinding stores the opaque binding of one hosted
// Desktop authorization until expiresAt, in Unix milliseconds, scoped to the
// Desktop authorization routes.
func (c Cookies) AttachDesktopAuthorizationBinding(writer http.ResponseWriter, binding string, expiresAt int64) {
	c.set(writer, BrowserDesktopAuthorizationCookieName, binding, c.desktopAuthorizationPath, expiresAt, true)
}

// AttachDesktopReauthenticationBinding stores the opaque binding of one hosted
// Desktop reauthentication until expiresAt, in Unix milliseconds, scoped to the
// Desktop reauthentication routes.
func (c Cookies) AttachDesktopReauthenticationBinding(writer http.ResponseWriter, binding string, expiresAt int64) {
	c.set(writer, BrowserDesktopReauthenticationCookieName, binding, c.desktopReauthenticationPath, expiresAt, true)
}

// ClearDesktopReauthenticationBinding expires the Desktop reauthentication
// binding once the flow completes or is abandoned.
func (c Cookies) ClearDesktopReauthenticationBinding(writer http.ResponseWriter) {
	c.expire(writer, BrowserDesktopReauthenticationCookieName, c.desktopReauthenticationPath, true)
}

// ClearDesktopAuthorizationBinding expires the Desktop authorization binding
// once the flow completes or is abandoned.
func (c Cookies) ClearDesktopAuthorizationBinding(writer http.ResponseWriter) {
	c.expire(writer, BrowserDesktopAuthorizationCookieName, c.desktopAuthorizationPath, true)
}

// AttachInvitationProof stores the second proof of a browser Invitation
// acceptance until expiresAt, in Unix milliseconds, scoped to the browser
// Invitation routes so no script and no other route can read it.
func (c Cookies) AttachInvitationProof(writer http.ResponseWriter, proof string, expiresAt int64) {
	c.set(writer, BrowserInvitationProofCookieName, proof, c.invitationPath, expiresAt, true)
}

// ClearInvitationProof expires the Invitation proof once it is redeemed or
// refused.
func (c Cookies) ClearInvitationProof(writer http.ResponseWriter) {
	c.expire(writer, BrowserInvitationProofCookieName, c.invitationPath, true)
}

// AttachExternalLoginBinding stores the browser binding of one external
// provider login until expiresAt, in Unix milliseconds, scoped to the provider
// routes that complete it.
func (c Cookies) AttachExternalLoginBinding(
	writer http.ResponseWriter,
	binding string,
	expiresAt int64,
) {
	c.set(
		writer,
		BrowserExternalLoginCookieName,
		binding,
		c.externalLoginPath,
		expiresAt,
		true,
	)
}

// ClearExternalLoginBinding expires the external login binding once the
// provider callback has been handled.
func (c Cookies) ClearExternalLoginBinding(writer http.ResponseWriter) {
	c.expire(
		writer,
		BrowserExternalLoginCookieName,
		c.externalLoginPath,
		true,
	)
}

// Attach stores a Session's access and refresh credentials and a fresh CSRF
// pair. The refresh cookie is scoped to the refresh route, and the CSRF token
// is the only cookie a script can read. Nil tokens write nothing.
func (c Cookies) Attach(
	writer http.ResponseWriter,
	tokens *model.AuthenticationTokens,
) {
	if tokens == nil {
		return
	}

	binding := model.NewCredentialToken()
	csrf := browserCSRFToken(binding)
	c.set(
		writer,
		BrowserAccessCookieName,
		tokens.AccessToken,
		"/",
		model.MillisFromTime(tokens.AccessExpiresAt),
		true,
	)
	c.set(
		writer,
		BrowserRefreshCookieName,
		tokens.RefreshToken,
		c.refreshPath,
		model.MillisFromTime(tokens.RefreshExpiresAt),
		true,
	)
	c.set(
		writer,
		BrowserCSRFBindingCookieName,
		binding,
		"/",
		model.MillisFromTime(tokens.RefreshExpiresAt),
		true,
	)
	c.set(
		writer,
		BrowserCSRFCookieName,
		csrf,
		"/",
		model.MillisFromTime(tokens.RefreshExpiresAt),
		false,
	)
}

// Clear expires the access, refresh and CSRF cookies. The kernel also does this
// when a cookie credential fails authentication.
func (c Cookies) Clear(writer http.ResponseWriter) {
	c.expire(writer, BrowserAccessCookieName, "/", true)
	c.expire(writer, BrowserRefreshCookieName, c.refreshPath, true)
	c.expire(writer, BrowserCSRFBindingCookieName, "/", true)
	c.expire(writer, BrowserCSRFCookieName, "/", false)
}

func (c Cookies) verifyCSRF(request *http.Request) error {
	binding, appErr := CookieValue(request, BrowserCSRFBindingCookieName)
	if appErr != nil || binding == "" {
		return csrfError()
	}
	cookieToken, appErr := CookieValue(request, BrowserCSRFCookieName)
	if appErr != nil || cookieToken == "" {
		return csrfError()
	}
	headerToken := request.Header.Get(BrowserCSRFHeader)
	expected := browserCSRFToken(binding)
	if headerToken == "" ||
		subtle.ConstantTimeCompare([]byte(cookieToken), []byte(expected)) != 1 ||
		subtle.ConstantTimeCompare([]byte(headerToken), []byte(expected)) != 1 {
		return csrfError()
	}
	return nil
}

func (c Cookies) set(
	writer http.ResponseWriter,
	name string,
	value string,
	path string,
	expiresAt int64,
	httpOnly bool,
) {
	expires := time.UnixMilli(expiresAt)
	maxAge := int(time.Until(expires).Seconds())
	if c.now != nil {
		maxAge = int(expires.Sub(c.now()).Seconds())
	}
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(writer, &http.Cookie{ //nolint:gosec // G124: Secure follows the public URL's scheme; HttpOnly is off only for the CSRF token clients read.
		Name:     name,
		Value:    value,
		Path:     path,
		MaxAge:   maxAge,
		Expires:  expires,
		HttpOnly: httpOnly,
		Secure:   c.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (c Cookies) expire(
	writer http.ResponseWriter,
	name string,
	path string,
	httpOnly bool,
) {
	http.SetCookie(writer, &http.Cookie{ //nolint:gosec // G124: Secure follows the public URL's scheme; HttpOnly is off only for the CSRF token clients read.
		Name:     name,
		Value:    "",
		Path:     path,
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
		HttpOnly: httpOnly,
		Secure:   c.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func browserCSRFToken(binding string) string {
	mac := hmac.New(sha256.New, []byte(binding))
	_, _ = mac.Write([]byte(csrfMessage))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func csrfError() error {
	return apperror.New("authentication.csrf.invalid")
}
