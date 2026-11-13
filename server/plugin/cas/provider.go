// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package cas implements externalauth.Provider for a CAS server: it redirects
// the browser to the CAS login and validates the returned service ticket.
package cas

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/platform/externalauth"
	"github.com/sudosylabs/proctor/server/plugin"
)

// Settings is a CAS provider's settings document.
type Settings struct {
	BaseURL          string                    `json:"BaseURL"`
	ValidationPath   string                    `json:"ValidationPath"`
	Timeout          config.Duration           `json:"Timeout"`
	MaxResponseBytes int64                     `json:"MaxResponseBytes"`
	Claims           externalauth.ClaimMapping `json:"Claims"`
}

// Validate checks settings for a provider that may or may not create
// accounts.
func (s Settings) Validate(autoProvision bool) error {
	var problems externalauth.SettingsErrors
	externalauth.ValidateHTTPSURL("base_url", s.BaseURL, &problems)
	if parsed, _ := url.Parse(s.BaseURL); parsed != nil && (parsed.RawQuery != "" || parsed.Fragment != "") {
		problems.Add("base_url", "must not contain a query or fragment")
	}
	if s.ValidationPath == "" ||
		!strings.HasPrefix(s.ValidationPath, "/") ||
		strings.HasPrefix(s.ValidationPath, "//") ||
		strings.ContainsAny(s.ValidationPath, "?#\x00\r\n") {
		problems.Add("validation_path", "must be an absolute path without a query or fragment")
	}
	externalauth.ValidateHTTPBounds(s.Timeout.Duration, s.MaxResponseBytes, &problems)
	externalauth.ValidateClaims(s.Claims, autoProvision, &problems)
	return problems.Err()
}

// New builds the CAS provider definition describes. CAS takes no secrets.
func New(definition plugin.IdentityProviderDefinition) (plugin.IdentityProvider, error) {
	var settings Settings
	if err := externalauth.DecodeSettings(definition.Settings, &settings); err != nil {
		return nil, err
	}
	if definition.Secrets != nil {
		return nil, errors.New("invalid provider settings: secrets: CAS takes no secrets")
	}
	if err := settings.Validate(definition.AutoProvision); err != nil {
		return nil, err
	}
	base, err := url.Parse(settings.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base URL: %w", err)
	}
	loginURL := *base
	loginURL.Path = strings.TrimSuffix(base.Path, "/") + "/login"
	validateURL := *base
	validateURL.Path = strings.TrimSuffix(base.Path, "/") + settings.ValidationPath
	return &Provider{
		mapper:      externalauth.NewProfileMapper(definition.ID, definition.AutoProvision, settings.Claims),
		loginURL:    &loginURL,
		validateURL: &validateURL,
		client: externalauth.NewHTTPClient(
			settings.Timeout.Duration,
			settings.MaxResponseBytes,
		),
		maximumResponseBytes: settings.MaxResponseBytes,
		now:                  time.Now,
	}, nil
}

type Provider struct {
	mapper               *externalauth.ProfileMapper
	loginURL             *url.URL
	validateURL          *url.URL
	client               *http.Client
	maximumResponseBytes int64
	now                  func() time.Time
}

func (p *Provider) State(
	callback plugin.Callback,
) (string, error) {
	state, err := callback.SingleValue("state", 256)
	if err != nil || !model.IsValidCredentialToken(state) {
		return "", externalauth.Rejected(
			"resolve CAS callback state",
			plugin.ErrInvalidCallback,
		)
	}
	return state, nil
}

func (p *Provider) Begin(
	_ context.Context,
	request plugin.BeginRequest,
) (plugin.BeginResponse, error) {
	serviceURL, err := casServiceURL(request.CallbackURL, request.State)
	if err != nil || !model.IsValidCredentialToken(request.Proof) {
		return plugin.BeginResponse{}, externalauth.InvalidResponse(
			"construct CAS login",
			errors.New("invalid external authentication transaction"),
		)
	}
	loginURL := *p.loginURL
	query := loginURL.Query()
	query.Set("service", serviceURL)
	if request.FreshAuthentication {
		query.Set("renew", "true")
	}
	loginURL.RawQuery = query.Encode()
	return plugin.BeginResponse{RedirectURL: loginURL.String()}, nil
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
			"validate CAS callback",
			plugin.ErrInvalidCallback,
		)
	}

	ticket, err := request.Callback.SingleValue("ticket", 2048)
	if err != nil {
		return plugin.Assertion{}, externalauth.Rejected(
			"validate CAS callback",
			err,
		)
	}

	serviceURL, err := casServiceURL(request.CallbackURL, request.State)
	if err != nil {
		return plugin.Assertion{}, externalauth.InvalidResponse(
			"construct CAS validation",
			err,
		)
	}

	validationURL := *p.validateURL
	query := validationURL.Query()
	query.Set("service", serviceURL)
	query.Set("ticket", ticket)
	if !request.AuthenticationStartedAt.IsZero() {
		query.Set("renew", "true")
	}

	validationURL.RawQuery = query.Encode()

	httpRequest, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		validationURL.String(),
		nil,
	)
	if err != nil {
		return plugin.Assertion{}, externalauth.InvalidResponse(
			"construct CAS validation",
			err,
		)
	}

	httpRequest.Header.Set("Accept", "application/xml, text/xml")
	response, err := p.client.Do(httpRequest)
	if err != nil {
		return plugin.Assertion{}, externalauth.Unavailable(
			"validate CAS ticket",
			err,
		)
	}

	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, response.Body)
		return plugin.Assertion{}, externalauth.Unavailable(
			"validate CAS ticket",
			fmt.Errorf("provider status %d", response.StatusCode),
		)
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return plugin.Assertion{}, externalauth.InvalidResponse(
			"read CAS validation",
			err,
		)
	}

	if int64(len(body)) > p.maximumResponseBytes {
		return plugin.Assertion{}, externalauth.InvalidResponse(
			"read CAS validation",
			externalauth.ErrResponseTooLarge,
		)
	}

	if bytes.Contains(bytes.ToUpper(body), []byte("<!DOCTYPE")) {
		return plugin.Assertion{}, externalauth.InvalidResponse(
			"decode CAS validation",
			errors.New("document type is not allowed"),
		)
	}

	var envelope serviceResponse
	decoder := xml.NewDecoder(bytes.NewReader(body))
	decoder.Strict = true
	if err := decoder.Decode(&envelope); err != nil {
		return plugin.Assertion{}, externalauth.InvalidResponse(
			"decode CAS validation",
			err,
		)
	}

	if envelope.Failure != nil {
		return plugin.Assertion{}, externalauth.Rejected(
			"validate CAS ticket",
			errors.New("CAS rejected authentication"),
		)
	}

	if envelope.Success == nil {
		return plugin.Assertion{}, externalauth.InvalidResponse(
			"decode CAS validation",
			errors.New("missing authentication result"),
		)
	}

	values := make(
		map[string][]string,
		len(envelope.Success.Attributes.Values)+1,
	)
	values["user"] = []string{strings.TrimSpace(envelope.Success.User)}
	for _, attribute := range envelope.Success.Attributes.Values {
		name := attribute.XMLName.Local
		value := strings.TrimSpace(attribute.Value)
		if name == "" ||
			value == "" ||
			len(value) > 4096 ||
			len(values) >= 256 {
			continue
		}

		values[name] = append(values[name], value)
	}

	emailVerified := p.mapperEmailVerified(values)
	authenticatedAt := p.now()
	if !request.AuthenticationStartedAt.IsZero() {
		// renew is checked by CAS at ticket validation as well as login.
		// Use the transaction start as a conservative proof instant: the
		// service-bound ticket required primary credentials after that start.
		if request.AuthenticationStartedAt.After(authenticatedAt) {
			return plugin.Assertion{}, externalauth.Rejected("verify fresh CAS authentication", errors.New("fresh authentication time is invalid"))
		}

		authenticatedAt = request.AuthenticationStartedAt
	}

	return p.mapper.Assertion(
		values,
		emailVerified,
		authenticatedAt,
	)
}

func (p *Provider) mapperEmailVerified(values map[string][]string) bool {
	// CAS does not define a standard email-verification assertion. An operator
	// may explicitly trust released email data or map a boolean-like attribute.
	claims := p.mapperClaims()
	if claims.TrustEmail {
		return true
	}
	if claims.EmailVerifiedClaim == "" {
		return false
	}
	for _, value := range values[claims.EmailVerifiedClaim] {
		if strings.EqualFold(strings.TrimSpace(value), "true") {
			return true
		}
	}
	return false
}

func (p *Provider) mapperClaims() externalauth.ClaimMapping {
	return p.mapper.Claims()
}

func casServiceURL(callbackURL string, state string) (string, error) {
	if !model.IsValidCredentialToken(state) {
		return "", errors.New("CAS state is invalid")
	}

	parsed, err := url.Parse(callbackURL)
	if err != nil ||
		parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil ||
		parsed.Fragment != "" {
		return "", errors.New("CAS callback URL is invalid")
	}

	query := parsed.Query()
	query.Set("state", state)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

type serviceResponse struct {
	Success *authenticationSuccess `xml:"authenticationSuccess"`
	Failure *authenticationFailure `xml:"authenticationFailure"`
}

type authenticationSuccess struct {
	User       string     `xml:"user"`
	Attributes attributes `xml:"attributes"`
}

type authenticationFailure struct {
	Code string `xml:"code,attr"`
}

type attributes struct {
	Values []attribute `xml:",any"`
}

type attribute struct {
	XMLName xml.Name
	Value   string `xml:",chardata"`
}

var _ plugin.IdentityProvider = (*Provider)(nil)
