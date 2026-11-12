// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package oidc implements externalauth.Provider for an OpenID Connect issuer,
// using the authorization code flow with PKCE and a nonce.
package oidc

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	coreoidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/platform/externalauth"
	"github.com/sudosylabs/proctor/server/plugin"
)

type Factory struct{}

func NewFactory() Factory {
	return Factory{}
}

func (Factory) New(
	settings config.ExternalAuthenticationProvider,
) (plugin.IdentityProvider, error) {
	if settings.OIDC == nil {
		return nil, errors.New("OIDC settings are missing")
	}
	return &Provider{
		mapper:   externalauth.NewProfileMapper(settings),
		settings: *settings.OIDC,
		client: externalauth.NewHTTPClient(
			settings.OIDC.Timeout.Duration,
			settings.OIDC.MaxResponseBytes,
		),
		now: time.Now,
	}, nil
}

type Provider struct {
	mapper   *externalauth.ProfileMapper
	settings config.OIDCProvider
	client   *http.Client
	now      func() time.Time

	discoveryMutex sync.Mutex
	discovered     *coreoidc.Provider
}

func (p *Provider) State(
	callback plugin.Callback,
) (string, error) {
	state, err := callback.SingleValue("state", 256)
	if err != nil || !model.IsValidCredentialToken(state) {
		return "", externalauth.Rejected(
			"resolve OIDC callback state",
			plugin.ErrInvalidCallback,
		)
	}
	return state, nil
}

func (p *Provider) Begin(
	ctx context.Context,
	request plugin.BeginRequest,
) (plugin.BeginResponse, error) {
	if !validCallbackURL(request.CallbackURL) ||
		!model.IsValidCredentialToken(request.State) ||
		!model.IsValidCredentialToken(request.Proof) {
		return plugin.BeginResponse{}, externalauth.InvalidResponse(
			"construct OIDC authorization",
			errors.New("invalid external authentication transaction"),
		)
	}
	discovered, err := p.discover(ctx)
	if err != nil {
		return plugin.BeginResponse{}, err
	}
	oauthConfig := p.oauthConfig(discovered, request.CallbackURL)
	options := []oauth2.AuthCodeOption{
		oauth2.S256ChallengeOption(request.Proof),
		oauth2.SetAuthURLParam("nonce", transactionNonce(p.mapper.Descriptor().ID, request.Proof)),
	}
	if request.FreshAuthentication {
		options = append(options, oauth2.SetAuthURLParam("prompt", "login"), oauth2.SetAuthURLParam("max_age", "0"))
	}
	redirectURL := oauthConfig.AuthCodeURL(request.State, options...)
	return plugin.BeginResponse{RedirectURL: redirectURL}, nil
}

func (p *Provider) Complete(
	ctx context.Context,
	request plugin.CompleteRequest,
) (plugin.Assertion, error) {
	callbackState, err := p.State(request.Callback)
	if err != nil ||
		subtle.ConstantTimeCompare(
			[]byte(callbackState),
			[]byte(request.State),
		) != 1 {
		return plugin.Assertion{}, externalauth.Rejected(
			"validate OIDC callback",
			plugin.ErrInvalidCallback,
		)
	}

	providerError, err := request.Callback.OptionalSingleValue("error", 256)
	if err != nil {
		return plugin.Assertion{}, externalauth.Rejected("validate OIDC callback", err)
	}

	if providerError != "" {
		return plugin.Assertion{}, externalauth.Rejected(
			"complete OIDC authorization",
			errors.New("provider denied authorization"),
		)
	}

	code, err := request.Callback.SingleValue(
		"code",
		8192,
	)
	if err != nil ||
		!model.IsValidCredentialToken(request.Proof) ||
		!validCallbackURL(request.CallbackURL) {
		return plugin.Assertion{}, externalauth.Rejected(
			"validate OIDC callback",
			plugin.ErrInvalidCallback,
		)
	}

	discovered, err := p.discover(ctx)
	if err != nil {
		return plugin.Assertion{}, err
	}

	providerContext := coreoidc.ClientContext(ctx, p.client)
	oauthConfig := p.oauthConfig(discovered, request.CallbackURL)
	token, err := oauthConfig.Exchange(
		providerContext,
		code,
		oauth2.VerifierOption(request.Proof),
	)
	if err != nil {
		var retrieveError *oauth2.RetrieveError
		if errors.As(err, &retrieveError) &&
			retrieveError.Response != nil &&
			retrieveError.Response.StatusCode >= 400 &&
			retrieveError.Response.StatusCode < 500 {
			return plugin.Assertion{}, externalauth.Rejected(
				"exchange OIDC authorization code",
				err,
			)
		}

		return plugin.Assertion{}, externalauth.Unavailable(
			"exchange OIDC authorization code",
			err,
		)
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return plugin.Assertion{}, externalauth.InvalidResponse(
			"verify OIDC ID token",
			errors.New("provider omitted the ID token"),
		)
	}

	verifier := discovered.VerifierContext(
		providerContext,
		&coreoidc.Config{ClientID: p.settings.ClientID},
	)
	idToken, err := verifier.Verify(providerContext, rawIDToken)
	if err != nil {
		return plugin.Assertion{}, externalauth.InvalidResponse(
			"verify OIDC ID token",
			err,
		)
	}

	expectedNonce := transactionNonce(
		p.mapper.Descriptor().ID,
		request.Proof,
	)
	if subtle.ConstantTimeCompare(
		[]byte(idToken.Nonce),
		[]byte(expectedNonce),
	) != 1 {
		return plugin.Assertion{}, externalauth.Rejected(
			"verify OIDC nonce",
			errors.New("OIDC nonce mismatch"),
		)
	}

	if idToken.AccessTokenHash != "" {
		if verifyErr := idToken.VerifyAccessToken(token.AccessToken); verifyErr != nil {
			return plugin.Assertion{}, externalauth.InvalidResponse(
				"verify OIDC access token",
				verifyErr,
			)
		}
	}

	claims := make(map[string]json.RawMessage)
	if claimsErr := idToken.Claims(&claims); claimsErr != nil {
		return plugin.Assertion{}, externalauth.InvalidResponse(
			"decode OIDC ID token claims",
			claimsErr,
		)
	}

	if p.settings.UseUserInfo {
		userInfo, userInfoErr := discovered.UserInfo(
			providerContext,
			oauth2.StaticTokenSource(token),
		)
		if userInfoErr != nil {
			return plugin.Assertion{}, externalauth.Unavailable(
				"retrieve OIDC user information",
				userInfoErr,
			)
		}

		if subtle.ConstantTimeCompare(
			[]byte(userInfo.Subject),
			[]byte(idToken.Subject),
		) != 1 {
			return plugin.Assertion{}, externalauth.InvalidResponse(
				"verify OIDC user information",
				errors.New("userinfo subject does not match ID token"),
			)
		}

		userInfoClaims := make(map[string]json.RawMessage)
		if claimsErr := userInfo.Claims(&userInfoClaims); claimsErr != nil {
			return plugin.Assertion{}, externalauth.InvalidResponse(
				"decode OIDC user information",
				claimsErr,
			)
		}

		securityClaim := p.mapper.Claims().MultiFactorAttribute
		for name, value := range userInfoClaims {
			if name != "sub" &&
				name != "auth_time" &&
				name != "amr" &&
				name != "acr" &&
				name != securityClaim {
				claims[name] = value
			}
		}
	}

	encodedSubject, _ := json.Marshal(idToken.Subject)
	claims["sub"] = encodedSubject
	values := oidcClaimValues(claims)
	emailVerified := p.mapper.Claims().TrustEmail ||
		oidcBooleanClaim(
			claims[p.mapper.Claims().EmailVerifiedClaim],
		)
	authenticatedAt, err := authenticationTime(
		claims["auth_time"],
		idToken.IssuedAt,
		p.now(),
	)
	if err != nil {
		return plugin.Assertion{}, externalauth.InvalidResponse(
			"verify OIDC authentication time",
			err,
		)
	}

	if !request.AuthenticationStartedAt.IsZero() {
		// OIDC Core requires auth_time with max_age. Issued-at is never
		// accepted as substitute proof of a fresh end-user authentication.
		if len(claims["auth_time"]) == 0 ||
			authenticatedAt < request.AuthenticationStartedAt.Truncate(time.Second).UnixMilli() ||
			authenticatedAt > p.now().UnixMilli() {
			return plugin.Assertion{}, externalauth.Rejected("verify fresh OIDC authentication", errors.New("fresh authentication proof is unavailable"))
		}
	}

	return p.mapper.Assertion(
		values,
		emailVerified,
		time.UnixMilli(authenticatedAt),
	)
}

func (p *Provider) discover(
	ctx context.Context,
) (*coreoidc.Provider, error) {
	p.discoveryMutex.Lock()
	defer p.discoveryMutex.Unlock()
	if p.discovered != nil {
		return p.discovered, nil
	}

	providerContext := coreoidc.ClientContext(ctx, p.client)
	discovered, err := coreoidc.NewProvider(
		providerContext,
		p.settings.Issuer,
	)
	if err != nil {
		return nil, externalauth.Unavailable(
			"discover OIDC provider",
			err,
		)
	}

	var metadata struct {
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
		UserInfoEndpoint      string `json:"userinfo_endpoint"`
		JWKSURI               string `json:"jwks_uri"`
	}
	if err := discovered.Claims(&metadata); err != nil {
		return nil, externalauth.InvalidResponse(
			"decode OIDC discovery",
			err,
		)
	}

	for field, endpoint := range map[string]string{
		"authorization endpoint": metadata.AuthorizationEndpoint,
		"token endpoint":         metadata.TokenEndpoint,
		"JWKS endpoint":          metadata.JWKSURI,
	} {
		if !validProviderEndpoint(endpoint) {
			return nil, externalauth.InvalidResponse(
				"validate OIDC discovery",
				fmt.Errorf("%s is invalid", field),
			)
		}
	}

	if p.settings.UseUserInfo &&
		!validProviderEndpoint(metadata.UserInfoEndpoint) {
		return nil, externalauth.InvalidResponse(
			"validate OIDC discovery",
			errors.New("userinfo endpoint is invalid"),
		)
	}

	p.discovered = discovered
	return discovered, nil
}

func (p *Provider) oauthConfig(
	discovered *coreoidc.Provider,
	callbackURL string,
) oauth2.Config {
	return oauth2.Config{
		ClientID:     p.settings.ClientID,
		ClientSecret: p.settings.ClientSecret,
		Endpoint:     discovered.Endpoint(),
		RedirectURL:  callbackURL,
		Scopes:       append([]string(nil), p.settings.Scopes...),
	}
}

func transactionNonce(providerID string, proof string) string {
	digest := sha256.Sum256(
		[]byte("proctor.oidc_nonce\x00" + providerID + "\x00" + proof),
	)
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func oidcClaimValues(
	claims map[string]json.RawMessage,
) map[string][]string {
	values := make(map[string][]string, len(claims))
	for name, raw := range claims {
		var single string
		if err := json.Unmarshal(raw, &single); err == nil {
			if single != "" && len(single) <= 4096 {
				values[name] = []string{single}
			}
			continue
		}
		var multiple []string
		if err := json.Unmarshal(raw, &multiple); err == nil {
			for _, value := range multiple {
				if value != "" && len(value) <= 4096 {
					values[name] = append(values[name], value)
				}
				if len(values[name]) == 256 {
					break
				}
			}
		}
	}
	return values
}

func oidcBooleanClaim(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var value bool
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	var text string
	return json.Unmarshal(raw, &text) == nil &&
		strings.EqualFold(text, "true")
}

func authenticationTime(
	raw json.RawMessage,
	issuedAt time.Time,
	now time.Time,
) (int64, error) {
	result := issuedAt
	if len(raw) != 0 {
		var number json.Number
		if err := json.Unmarshal(raw, &number); err != nil {
			return 0, errors.New("auth_time is not numeric")
		}
		seconds, err := strconv.ParseInt(number.String(), 10, 64)
		if err != nil || seconds <= 0 {
			return 0, errors.New("auth_time is invalid")
		}
		result = time.Unix(seconds, 0)
	}
	if result.IsZero() ||
		result.After(now.Add(5*time.Minute)) {
		return 0, errors.New("authentication time is invalid")
	}
	return result.UnixMilli(), nil
}

func validCallbackURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil &&
		(parsed.Scheme == "http" || parsed.Scheme == "https") &&
		parsed.Host != "" &&
		parsed.User == nil &&
		parsed.RawQuery == "" &&
		parsed.Fragment == ""
}

func validProviderEndpoint(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil ||
		parsed.Host == "" ||
		parsed.User != nil ||
		parsed.Fragment != "" {
		return false
	}

	if parsed.Scheme == "https" {
		return true
	}
	if parsed.Scheme != "http" {
		return false
	}

	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}

	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

var (
	_ plugin.Plugin               = Plugin{}
	_ plugin.IdentityProviderType = providerType{}
	_ plugin.IdentityProvider     = (*Provider)(nil)
)
