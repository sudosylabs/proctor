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
	"io"
	"log/slog"
	"slices"

	"github.com/sudosylabs/proctor/server/plugin"
)

// attemptWorkspaceGrants lists the plugins that may use
// plugin.AttemptWorkspaces. The list is closed and owned by the core: the
// capability writes candidate work, so a plugin gets it by being named here
// in review, never by asking.
var attemptWorkspaceGrants = []string{"execution"}

// AttemptWorkspaceGranted reports whether the plugin with id may use
// plugin.AttemptWorkspaces.
func AttemptWorkspaceGranted(id string) bool {
	return slices.Contains(attemptWorkspaceGrants, id)
}

// AttemptWorkspaceService runs the Attempt Workspace use cases for a
// plugin. The host names the calling plugin on each call, so the service can
// act in its name; the service answers with the plugin package's types and
// errors.
type AttemptWorkspaceService interface {
	BindEnvironment(ctx context.Context, pluginID string, request plugin.BindEnvironmentRequest) (plugin.EnvironmentBindResult, error)
	EndEnvironmentBinding(ctx context.Context, pluginID, attemptID, bindingID string) (plugin.EnvironmentBinding, error)
	ApplyBatch(ctx context.Context, pluginID string, batch plugin.WorkspaceBatch) (plugin.WorkspaceBatchResult, error)
	List(ctx context.Context, pluginID string, request plugin.WorkspaceListRequest) (plugin.WorkspacePage, error)
	ListJournal(ctx context.Context, pluginID string, request plugin.WorkspaceJournalRequest) (plugin.WorkspaceJournalPage, error)
	OpenFile(ctx context.Context, pluginID string, environment plugin.BoundEnvironment, entryID string) (plugin.WorkspaceFile, error)
}

// AttemptWorkspaces returns the Attempt Workspace capability, scoped to
// this plugin and usable while this activation lasts, for a plugin the core
// grants it to.
func (r *registrar) AttemptWorkspaces() plugin.AttemptWorkspaces {
	return pluginWorkspaces{
		service:    r.host.attemptWorkspaces,
		activation: r.activation,
		granted:    AttemptWorkspaceGranted(r.entry.manifest.ID),
	}
}

type pluginWorkspaces struct {
	service    AttemptWorkspaceService
	activation *activation
	granted    bool
}

var _ plugin.AttemptWorkspaces = pluginWorkspaces{}

func (w pluginWorkspaces) available() error {
	if !w.granted || w.service == nil || !w.activation.usable() {
		return fmt.Errorf("plugin %q attempt workspaces: %w", w.activation.entry.manifest.ID, plugin.ErrUnavailable)
	}
	return nil
}

func (w pluginWorkspaces) id() string { return w.activation.entry.manifest.ID }

func (w pluginWorkspaces) BindEnvironment(ctx context.Context, request plugin.BindEnvironmentRequest) (plugin.EnvironmentBindResult, error) {
	if err := w.available(); err != nil {
		return plugin.EnvironmentBindResult{}, err
	}
	return w.service.BindEnvironment(ctx, w.id(), request)
}

func (w pluginWorkspaces) EndEnvironmentBinding(ctx context.Context, attemptID, bindingID string) (plugin.EnvironmentBinding, error) {
	if err := w.available(); err != nil {
		return plugin.EnvironmentBinding{}, err
	}
	return w.service.EndEnvironmentBinding(ctx, w.id(), attemptID, bindingID)
}

func (w pluginWorkspaces) ApplyBatch(ctx context.Context, batch plugin.WorkspaceBatch) (plugin.WorkspaceBatchResult, error) {
	if err := w.available(); err != nil {
		return plugin.WorkspaceBatchResult{}, err
	}
	batch.Mutations = slices.Clone(batch.Mutations)
	if batch.Content != nil {
		batch.Content = guardedWorkspaceContent{entry: w.activation.entry, inner: batch.Content}
	}
	return w.service.ApplyBatch(ctx, w.id(), batch)
}

func (w pluginWorkspaces) List(ctx context.Context, request plugin.WorkspaceListRequest) (plugin.WorkspacePage, error) {
	if err := w.available(); err != nil {
		return plugin.WorkspacePage{}, err
	}
	return w.service.List(ctx, w.id(), request)
}

func (w pluginWorkspaces) ListJournal(ctx context.Context, request plugin.WorkspaceJournalRequest) (plugin.WorkspaceJournalPage, error) {
	if err := w.available(); err != nil {
		return plugin.WorkspaceJournalPage{}, err
	}
	return w.service.ListJournal(ctx, w.id(), request)
}

func (w pluginWorkspaces) OpenFile(ctx context.Context, environment plugin.BoundEnvironment, entryID string) (plugin.WorkspaceFile, error) {
	if err := w.available(); err != nil {
		return plugin.WorkspaceFile{}, err
	}
	return w.service.OpenFile(ctx, w.id(), environment, entryID)
}

// guardedWorkspaceContent contains a panic in the plugin's content source,
// which the core calls while it stages a batch.
type guardedWorkspaceContent struct {
	entry *entry
	inner plugin.WorkspaceContent
}

func (c guardedWorkspaceContent) Next(ctx context.Context) (io.Reader, error) {
	var reader io.Reader
	err := c.entry.call("workspace content", func() error {
		var nextErr error
		reader, nextErr = c.inner.Next(ctx)
		return nextErr
	})
	if err != nil {
		return nil, err
	}
	return guardedReader{entry: c.entry, inner: reader}, nil
}

type guardedReader struct {
	entry *entry
	inner io.Reader
}

// Read contains a panic like call does, but without reporting every read to
// the Recorder.
func (r guardedReader) Read(buffer []byte) (count int, err error) {
	if r.inner == nil {
		return 0, io.ErrUnexpectedEOF
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			r.entry.logger.Error("plugin panicked", slog.String("operation", "read workspace content"))
			r.entry.recordPanic()
			count, err = 0, fmt.Errorf("plugin %q panicked during read workspace content: %w", r.entry.manifest.ID, plugin.ErrUnavailable)
		}
	}()
	return r.inner.Read(buffer)
}
