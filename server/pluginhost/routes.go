// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package pluginhost

import (
	"context"
	"fmt"
	"slices"

	"github.com/sudosylabs/proctor/server/plugin"
)

// MountedRoute is one route an active plugin serves. Its Handler or Socket
// contains the plugin's panics, answers plugin.ErrUnavailable once the
// plugin has failed, and ends its context when the plugin's activation
// ends. Available reports whether that activation still serves, so a
// consumer can answer 404 the moment the plugin fails, before the next
// change notice.
type MountedRoute struct {
	Plugin    string
	Route     plugin.Route
	Available func() bool
}

type registeredRoute struct {
	entry      *entry
	activation *activation
	route      plugin.Route
}

// AddRoute records route for the plugin being activated.
func (r *registrar) AddRoute(route plugin.Route) error {
	if err := route.Validate(); err != nil {
		return err
	}
	if !plugin.IsRoutableID(r.entry.manifest.ID) {
		return fmt.Errorf("plugin %q cannot serve routes: its ID is not a single path segment", r.entry.manifest.ID)
	}
	registered := &registeredRoute{entry: r.entry, activation: r.activation, route: route}
	return r.register("routes", func(c *contributions) error {
		for _, existing := range c.routes {
			if existing.entry == r.entry && existing.route.Method == route.Method &&
				existing.route.Guard == route.Guard && existing.route.Path == route.Path {
				return fmt.Errorf("plugin %q already serves %s %s", r.entry.manifest.ID, route.Method, route.Path)
			}
		}
		c.routes = append(c.routes, registered)
		return nil
	})
}

// Routes returns every active plugin's routes, in activation and
// registration order.
func (h *Host) Routes() []MountedRoute {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	registered := slices.Clone(h.contributions.routes)
	h.mu.Unlock()
	mounted := make([]MountedRoute, 0, len(registered))
	for _, item := range registered {
		if item.entry.currentState() != StateActive || !item.activation.usable() {
			continue
		}
		mounted = append(mounted, MountedRoute{
			Plugin:    item.entry.manifest.ID,
			Route:     item.guarded(),
			Available: item.available,
		})
	}
	return mounted
}

func (r *registeredRoute) available() bool {
	return r.entry.currentState() == StateActive && r.activation.usable()
}

// guarded returns the route with its handler wrapped.
func (r *registeredRoute) guarded() plugin.Route {
	route := r.route
	if inner := r.route.Handler; inner != nil {
		route.Handler = func(ctx context.Context, access plugin.Access, request plugin.Request) (plugin.Response, error) {
			bounded, cancel := r.activation.bind(ctx)
			defer cancel()
			if !r.activation.usable() {
				return plugin.Response{}, fmt.Errorf("plugin %q: %w", r.entry.manifest.ID, plugin.ErrUnavailable)
			}
			return serveValue(r.entry, "serve route", func() (plugin.Response, error) {
				return inner(bounded, access, request)
			})
		}
	}
	if inner := r.route.Socket; inner != nil {
		route.Socket = func(ctx context.Context, access plugin.Access, socket plugin.Socket) error {
			bounded, cancel := r.activation.bind(ctx)
			defer cancel()
			if !r.activation.usable() {
				return fmt.Errorf("plugin %q: %w", r.entry.manifest.ID, plugin.ErrUnavailable)
			}
			return r.entry.serve("serve socket", func() error { return inner(bounded, access, socket) })
		}
	}
	return route
}

// bind returns a context that ends with ctx or with the activation,
// whichever ends first.
func (a *activation) bind(ctx context.Context) (context.Context, context.CancelFunc) {
	bounded, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(a.ctx, cancel)
	return bounded, func() {
		stop()
		cancel()
	}
}
