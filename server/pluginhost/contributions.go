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
	"io"
	"slices"
	"strings"

	"github.com/sudosylabs/proctor/server/plugin"
)

// contributions is what active plugins registered, in activation order.
type contributions struct {
	identityTypes []*guardedIdentityProviderType
	exportFormats []*guardedExportFormat
	eventHandlers []EventSubscription
}

// discard forgets what e registered.
func (c *contributions) discard(e *entry) {
	c.identityTypes = slices.DeleteFunc(c.identityTypes, func(t *guardedIdentityProviderType) bool { return t.entry == e })
	c.exportFormats = slices.DeleteFunc(c.exportFormats, func(f *guardedExportFormat) bool { return f.entry == e })
	c.eventHandlers = slices.DeleteFunc(c.eventHandlers, func(s EventSubscription) bool { return s.Plugin == e.manifest.ID })
}

// register runs add for the plugin being activated while its Activate is
// still running.
func (r *registrar) register(kind string, add func(*contributions) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.open {
		return fmt.Errorf("%s are added during Activate", kind)
	}
	r.host.mu.Lock()
	defer r.host.mu.Unlock()
	return add(&r.host.contributions)
}

// AddExportFormat records format for the plugin being activated.
func (r *registrar) AddExportFormat(format plugin.ExportFormat) error {
	if format == nil {
		return errors.New("export format is nil")
	}
	var mediaType, extension string
	if err := r.entry.call("export format", func() error {
		mediaType, extension = format.MediaType(), format.FileExtension()
		return nil
	}); err != nil {
		return err
	}
	if mediaType == "" || !strings.HasPrefix(extension, ".") {
		return errors.New("export format needs a media type and a file extension starting with a dot")
	}
	guarded := &guardedExportFormat{entry: r.entry, mediaType: mediaType, extension: extension, inner: format}
	return r.register("export formats", func(c *contributions) error {
		for _, existing := range c.exportFormats {
			if existing.mediaType == mediaType {
				return fmt.Errorf("export format %q is already offered by plugin %q", mediaType, existing.entry.manifest.ID)
			}
		}
		c.exportFormats = append(c.exportFormats, guarded)
		return nil
	})
}

// ExportFormat returns the active export format for mediaType. It contains
// its plugin's panics and answers plugin.ErrUnavailable once the plugin has
// failed.
func (h *Host) ExportFormat(mediaType string) (plugin.ExportFormat, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, format := range h.contributions.exportFormats {
		if format.mediaType == mediaType && format.entry.currentState() == StateActive {
			return format, true
		}
	}
	return nil, false
}

type guardedExportFormat struct {
	entry     *entry
	mediaType string
	extension string
	inner     plugin.ExportFormat
}

func (f *guardedExportFormat) MediaType() string     { return f.mediaType }
func (f *guardedExportFormat) FileExtension() string { return f.extension }

func (f *guardedExportFormat) Write(ctx context.Context, archive plugin.ExportArchive, w io.Writer) error {
	return f.entry.serve("write export", func() error { return f.inner.Write(ctx, archive, w) })
}

// EventSubscription is one plugin's handler for one topic.
type EventSubscription struct {
	// Key identifies the subscription across deliveries: the plugin ID and
	// the topic.
	Key     string
	Plugin  string
	Topic   plugin.EventTopic
	Handler plugin.EventHandler
}

// AddEventHandler subscribes handler to topic for the plugin being
// activated.
func (r *registrar) AddEventHandler(topic plugin.EventTopic, handler plugin.EventHandler) error {
	if handler == nil {
		return errors.New("event handler is nil")
	}
	if topic == "" {
		return errors.New("event topic is empty")
	}
	key := r.entry.manifest.ID + ":" + string(topic)
	subscription := EventSubscription{
		Key:     key,
		Plugin:  r.entry.manifest.ID,
		Topic:   topic,
		Handler: guardedEventHandler{entry: r.entry, inner: handler},
	}
	return r.register("event handlers", func(c *contributions) error {
		for _, existing := range c.eventHandlers {
			if existing.Key == key {
				return fmt.Errorf("plugin %q already handles topic %q", r.entry.manifest.ID, topic)
			}
		}
		c.eventHandlers = append(c.eventHandlers, subscription)
		return nil
	})
}

// EventSubscriptions returns every active plugin's event handlers, in
// activation order. Each handler contains its plugin's panics and answers
// plugin.ErrUnavailable once the plugin has failed, so the event is offered
// again later.
func (h *Host) EventSubscriptions() []EventSubscription {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.contributions.eventHandlers)
}

type guardedEventHandler struct {
	entry *entry
	inner plugin.EventHandler
}

func (g guardedEventHandler) HandleEvent(ctx context.Context, event plugin.Event) error {
	return g.entry.serve("handle event", func() error { return g.inner.HandleEvent(ctx, event) })
}
