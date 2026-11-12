// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package externalauth is what the application sees of an external sign-in
// provider. Provider is one configured provider: it starts a sign-in, completes
// the callback and returns a normalized assertion about the person. Registry
// holds the enabled providers and is rebuilt from configuration on reload.
//
// Finding or creating the User, audit and the Session stay in app/identity.
// The protocols themselves are the child packages cas and oidc.
package externalauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/plugin"
)

// A Provider fails with one of these; NewProvider maps the matching
// plugin.Err* sentinels onto them.
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

type BeginRequest struct {
	CallbackURL         string
	State               string
	Proof               string
	FreshAuthentication bool
}

type BeginResponse struct {
	RedirectURL string
}

type CompleteRequest struct {
	CallbackURL             string
	State                   string
	Proof                   string
	Callback                model.ExternalAuthenticationCallback
	AuthenticationStartedAt time.Time
}

// Provider owns one configured protocol integration. It returns only a
// normalized assertion; account resolution, provisioning, auditing, and
// session creation remain in the application layer.
type Provider interface {
	Descriptor() model.ExternalAuthenticationProvider
	AutoProvision() bool
	Begin(context.Context, BeginRequest) (*BeginResponse, error)
	State(model.ExternalAuthenticationCallback) (string, error)
	Complete(
		context.Context,
		CompleteRequest,
	) (*model.ExternalAuthenticationAssertion, error)
}

// Registry holds the configured providers, built from the sign-in protocols
// the plugins offer. Configure rebuilds them from configuration, including on
// reload; there is no process-wide provider map or init-time side effect.
type Registry struct {
	mutex     sync.RWMutex
	factories map[string]plugin.IdentityProviderType
	providers map[string]Provider
}

// NewRegistry returns a Registry that builds providers with factories, one
// per protocol Type.
func NewRegistry(factories ...plugin.IdentityProviderType) (*Registry, error) {
	registry := &Registry{
		factories: make(map[string]plugin.IdentityProviderType, len(factories)),
		providers: make(map[string]Provider),
	}
	for _, factory := range factories {
		if factory == nil {
			return nil, errors.New("external authentication factory is nil")
		}
		providerType := strings.TrimSpace(factory.Type())
		if providerType == "" {
			return nil, errors.New("external authentication factory type is empty")
		}
		if _, exists := registry.factories[providerType]; exists {
			return nil, fmt.Errorf(
				"external authentication factory %q is duplicated",
				providerType,
			)
		}
		registry.factories[providerType] = factory
	}
	return registry, nil
}

func (r *Registry) Configure(
	settings config.ExternalAuthentication,
) error {
	configured := make(map[string]Provider)
	for _, definition := range settings.Providers {
		if !definition.Enabled {
			continue
		}

		factory, exists := r.factories[definition.Type]
		if !exists {
			return fmt.Errorf(
				"external authentication provider %q uses unregistered type %q",
				definition.ID,
				definition.Type,
			)
		}

		settings, err := json.Marshal(definition)
		if err != nil {
			return fmt.Errorf(
				"encode external authentication provider %q settings: %w",
				definition.ID,
				err,
			)
		}

		provider, err := factory.New(plugin.IdentityProviderDefinition{ID: definition.ID, Settings: settings})
		if err != nil {
			return fmt.Errorf(
				"configure external authentication provider %q: %w",
				definition.ID,
				err,
			)
		}

		if provider == nil {
			return fmt.Errorf(
				"external authentication provider %q factory returned nil",
				definition.ID,
			)
		}

		if _, exists := configured[definition.ID]; exists {
			return fmt.Errorf(
				"external authentication provider %q is duplicated",
				definition.ID,
			)
		}

		configured[definition.ID] = NewProvider(definition, provider)
	}

	r.mutex.Lock()
	r.providers = configured
	r.mutex.Unlock()
	return nil
}

func (r *Registry) Provider(id string) (Provider, bool) {
	r.mutex.RLock()
	defer r.mutex.RUnlock()
	provider, exists := r.providers[id]
	return provider, exists
}

func (r *Registry) Descriptors() []model.ExternalAuthenticationProvider {
	r.mutex.RLock()
	defer r.mutex.RUnlock()
	descriptors := make(
		[]model.ExternalAuthenticationProvider,
		0,
		len(r.providers),
	)
	for _, provider := range r.providers {
		descriptors = append(descriptors, provider.Descriptor())
	}
	sort.Slice(descriptors, func(left, right int) bool {
		return descriptors[left].ID < descriptors[right].ID
	})
	return descriptors
}

// OperationError exposes a stable classification without allowing provider
// URLs, authorization codes, tickets, tokens, claims, or response bodies from
// the wrapped diagnostic error to appear through Error(). Kind is one of the
// plugin.Err* sentinels.
type OperationError struct {
	Kind      error
	Operation string
	Cause     error
}

func (e *OperationError) Error() string {
	return e.Operation + ": provider operation failed"
}

func (e *OperationError) Unwrap() error {
	return e.Cause
}

func (e *OperationError) Is(target error) bool {
	return target == e.Kind || errors.Is(e.Cause, target)
}

// Rejected classifies cause as plugin.ErrAuthenticationRejected.
func Rejected(operation string, cause error) error {
	return &OperationError{
		Kind:      plugin.ErrAuthenticationRejected,
		Operation: operation,
		Cause:     cause,
	}
}

// InvalidResponse classifies cause as plugin.ErrInvalidResponse.
func InvalidResponse(operation string, cause error) error {
	return &OperationError{
		Kind:      plugin.ErrInvalidResponse,
		Operation: operation,
		Cause:     cause,
	}
}

// Unavailable classifies cause as plugin.ErrProviderUnavailable.
func Unavailable(operation string, cause error) error {
	return &OperationError{
		Kind:      plugin.ErrProviderUnavailable,
		Operation: operation,
		Cause:     cause,
	}
}
