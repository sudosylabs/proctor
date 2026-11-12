// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// An IdentityProvider fails with one of these, wrapped as it likes, so Proctor
// can classify the failure without reading the provider's diagnostics.
var (
	// ErrAuthenticationRejected means the provider or the person declined
	// the authentication.
	ErrAuthenticationRejected = errors.New("external authentication rejected")
	// ErrInvalidResponse means the provider's response failed validation.
	ErrInvalidResponse = errors.New("external authentication response is invalid")
	// ErrProviderUnavailable means the provider could not be reached or
	// failed to answer.
	ErrProviderUnavailable = errors.New("external authentication provider is unavailable")
)

// ErrInvalidCallback is returned by Callback.SingleValue when a value is
// missing, repeated, too long or contains a NUL, CR or LF.
var ErrInvalidCallback = errors.New("external authentication callback is invalid")

// IdentityProvider authenticates a person with one external protocol and
// returns only a normalized Assertion. Account resolution, provisioning,
// audit and Sessions stay in Proctor.
//
// Begin returns the redirect that starts authentication. State extracts the
// state value from a callback so Proctor can find and consume the matching
// login exactly once before Complete validates the provider's credential.
type IdentityProvider interface {
	Begin(context.Context, BeginRequest) (BeginResponse, error)
	State(Callback) (string, error)
	Complete(context.Context, CompleteRequest) (Assertion, error)
}

// IdentityProviderType builds the identity providers of one sign-in
// protocol. A plugin registers it with Host.AddIdentityProviderType; each
// configured provider whose Type matches is then built with New, again
// whenever the configuration is reloaded.
type IdentityProviderType interface {
	// Type is the protocol name a configured provider selects, for example
	// "oidc". It is unique among the compiled-in plugins.
	Type() string
	// New builds the provider definition describes. It validates the
	// settings it reads and fails rather than guess.
	New(definition IdentityProviderDefinition) (IdentityProvider, error)
}

// IdentityProviderDefinition is one configured identity provider.
type IdentityProviderDefinition struct {
	// ID is the provider's configured identifier.
	ID string
	// Settings is the provider's configuration document, as it appears in
	// the server's external authentication settings. Its shape belongs to
	// the protocol.
	Settings json.RawMessage
}

// BeginRequest asks a provider to start authentication.
type BeginRequest struct {
	// CallbackURL is where the provider sends the browser back.
	CallbackURL string
	// State is the opaque value the provider must return in the callback.
	State string
	// Proof is a secret bound to the initiating browser that the provider
	// can tie the transaction to, for example as a PKCE verifier. It is the
	// same value in CompleteRequest.
	Proof string
	// FreshAuthentication asks the provider to authenticate the person
	// again rather than reuse an existing provider session.
	FreshAuthentication bool
}

// BeginResponse is where the browser goes to authenticate.
type BeginResponse struct {
	RedirectURL string
}

// Callback is a bounded, transport-neutral view of the values the provider
// returned. The provider, not Proctor, owns the meaning of each value.
type Callback struct {
	Values map[string][]string
}

// SingleValue returns the one non-empty value of name. It returns
// ErrInvalidCallback when the value is absent, repeated, empty, longer than
// maxLen bytes or contains a NUL, CR or LF.
func (c Callback) SingleValue(name string, maxLen int) (string, error) {
	values := c.Values[name]
	if len(values) != 1 || values[0] == "" ||
		len(values[0]) > maxLen ||
		strings.ContainsAny(values[0], "\x00\r\n") {
		return "", ErrInvalidCallback
	}
	return values[0], nil
}

// OptionalSingleValue returns the named value when it is present, or "" when
// it is absent. A present value follows SingleValue's rules, except that it
// may be empty.
func (c Callback) OptionalSingleValue(name string, maxLen int) (string, error) {
	values, exists := c.Values[name]
	if !exists {
		return "", nil
	}
	if len(values) != 1 || len(values[0]) > maxLen ||
		strings.ContainsAny(values[0], "\x00\r\n") {
		return "", ErrInvalidCallback
	}
	return values[0], nil
}

// CompleteRequest asks a provider to validate a callback.
type CompleteRequest struct {
	CallbackURL string
	State       string
	Proof       string
	Callback    Callback
	// AuthenticationStartedAt is when Begin ran. A provider that requires
	// fresh authentication rejects an assertion authenticated before it.
	AuthenticationStartedAt time.Time
}

// AuthenticationStrength is how strongly the provider authenticated the
// person.
type AuthenticationStrength string

const (
	// AuthenticationSingleFactor is one authentication factor.
	AuthenticationSingleFactor AuthenticationStrength = "single_factor"
	// AuthenticationMultiFactor is two or more authentication factors.
	AuthenticationMultiFactor AuthenticationStrength = "multi_factor"
)

// Assertion is the normalized result of a successful authentication.
//
// Subject is the provider's opaque, stable identifier for the person. Proctor
// stores it only to link the external identity; it is never returned,
// logged or audited.
type Assertion struct {
	Subject                string
	Username               string
	Email                  string
	EmailVerified          bool
	DisplayName            string
	FirstName              string
	LastName               string
	HomeOrganization       string
	Affiliations           []string
	AuthenticationStrength AuthenticationStrength
	AuthenticatedAt        time.Time
}
