// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sudosylabs/proctor/server/plugin"
)

// AddIdentityProviderType records providerType for the plugin being
// activated.
func (r *registrar) AddIdentityProviderType(providerType plugin.IdentityProviderType) error {
	if providerType == nil {
		return errors.New("identity provider type is nil")
	}
	var name string
	if err := r.entry.call("identity provider type", func() error {
		name = strings.TrimSpace(providerType.Type())
		return nil
	}); err != nil {
		return err
	}
	if name == "" {
		return errors.New("identity provider type name is empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.open {
		return errors.New("identity provider types are added during Activate")
	}
	return r.host.claimIdentityProviderType(name, &guardedIdentityProviderType{entry: r.entry, name: name, inner: providerType})
}

// claimIdentityProviderType keeps providerType unless another plugin already
// offers name.
func (h *Host) claimIdentityProviderType(name string, providerType *guardedIdentityProviderType) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, existing := range h.identityTypes {
		if existing.name == name {
			return fmt.Errorf("identity provider type %q is already offered by plugin %q", name, existing.entry.manifest.ID)
		}
	}
	h.identityTypes = append(h.identityTypes, providerType)
	return nil
}

// IdentityProviderTypes returns the sign-in protocols that active plugins
// offer, in activation order. Each one contains its plugin's panics and
// answers plugin.ErrProviderUnavailable once the plugin has failed.
func (h *Host) IdentityProviderTypes() []plugin.IdentityProviderType {
	h.mu.Lock()
	defer h.mu.Unlock()
	types := make([]plugin.IdentityProviderType, 0, len(h.identityTypes))
	for _, providerType := range h.identityTypes {
		if providerType.entry.currentState() == StateActive {
			types = append(types, providerType)
		}
	}
	return types
}

type guardedIdentityProviderType struct {
	entry *entry
	name  string
	inner plugin.IdentityProviderType
}

func (t *guardedIdentityProviderType) Type() string { return t.name }

func (t *guardedIdentityProviderType) New(definition plugin.IdentityProviderDefinition) (plugin.IdentityProvider, error) {
	var provider plugin.IdentityProvider
	err := t.entry.serve("build identity provider", func() error {
		var buildErr error
		provider, buildErr = t.inner.New(definition)
		return buildErr
	})
	if err != nil {
		return nil, err
	}
	if provider == nil {
		return nil, fmt.Errorf("identity provider type %q built no provider", t.name)
	}
	return &guardedIdentityProvider{entry: t.entry, inner: provider}, nil
}

// guardedIdentityProvider contains a panic in any provider call and reports
// a failed plugin as an unavailable provider.
type guardedIdentityProvider struct {
	entry *entry
	inner plugin.IdentityProvider
}

func (p *guardedIdentityProvider) Begin(ctx context.Context, request plugin.BeginRequest) (plugin.BeginResponse, error) {
	var response plugin.BeginResponse
	err := p.entry.serve("begin authentication", func() error {
		var beginErr error
		response, beginErr = p.inner.Begin(ctx, request)
		return beginErr
	})
	return response, providerFailure(err)
}

func (p *guardedIdentityProvider) State(callback plugin.Callback) (string, error) {
	var state string
	err := p.entry.serve("read authentication state", func() error {
		var stateErr error
		state, stateErr = p.inner.State(callback)
		return stateErr
	})
	return state, providerFailure(err)
}

func (p *guardedIdentityProvider) Complete(ctx context.Context, request plugin.CompleteRequest) (plugin.Assertion, error) {
	var assertion plugin.Assertion
	err := p.entry.serve("complete authentication", func() error {
		var completeErr error
		assertion, completeErr = p.inner.Complete(ctx, request)
		return completeErr
	})
	return assertion, providerFailure(err)
}

// providerFailure classifies a contained panic or a failed plugin as an
// unavailable provider, and leaves the provider's own errors as they are.
func providerFailure(err error) error {
	if errors.Is(err, plugin.ErrUnavailable) && !errors.Is(err, plugin.ErrProviderUnavailable) {
		return fmt.Errorf("%w: %w", plugin.ErrProviderUnavailable, err)
	}
	return err
}
