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

	guarded := &guardedIdentityProviderType{entry: r.entry, name: name, inner: providerType}
	return r.register("identity provider types", func(c *contributions) error {
		for _, existing := range c.identityTypes {
			if existing.name == name {
				return fmt.Errorf("identity provider type %q is already offered by plugin %q", name, existing.entry.manifest.ID)
			}
		}
		c.identityTypes = append(c.identityTypes, guarded)
		return nil
	})
}

// IdentityProviderTypes returns the sign-in protocols that active plugins
// offer, in activation order. Each one contains its plugin's panics and
// answers plugin.ErrProviderUnavailable once the plugin has failed.
func (h *Host) IdentityProviderTypes() []plugin.IdentityProviderType {
	h.mu.Lock()
	defer h.mu.Unlock()
	types := make([]plugin.IdentityProviderType, 0, len(h.contributions.identityTypes))
	for _, providerType := range h.contributions.identityTypes {
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
	provider, err := serveValue(t.entry, "build identity provider", func() (plugin.IdentityProvider, error) {
		return t.inner.New(definition)
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
	response, err := serveValue(p.entry, "begin authentication", func() (plugin.BeginResponse, error) {
		return p.inner.Begin(ctx, request)
	})
	return response, providerFailure(err)
}

func (p *guardedIdentityProvider) State(callback plugin.Callback) (string, error) {
	state, err := serveValue(p.entry, "read authentication state", func() (string, error) {
		return p.inner.State(callback)
	})
	return state, providerFailure(err)
}

func (p *guardedIdentityProvider) Complete(ctx context.Context, request plugin.CompleteRequest) (plugin.Assertion, error) {
	assertion, err := serveValue(p.entry, "complete authentication", func() (plugin.Assertion, error) {
		return p.inner.Complete(ctx, request)
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
