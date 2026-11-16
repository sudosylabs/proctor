// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package executionhost

import (
	"context"
	"errors"
	"io"

	"github.com/sudosylabs/execenv"

	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/model"
)

// machine is one execenv grant behind the port's Machine.
type machine struct {
	env    execenv.Env
	hosts  *Hosts
	host   string
	client execenv.Host
}

var _ environment.Machine = (*machine)(nil)

func (m *machine) GrantID() string { return string(m.env.ID()) }
func (m *machine) Epoch() string   { return m.env.Epoch() }

// fail translates err, forgetting the client when its connection broke.
func (m *machine) fail(operation string, err error) error {
	if errors.Is(err, execenv.ErrConnection) {
		m.hosts.broken(m.host, m.client)
	}
	return translate(operation, err)
}

func toFence(fence environment.Fence) execenv.ProjectionFence {
	return execenv.ProjectionFence{
		ExecutionGrantID: execenv.ID(fence.GrantID), EnvironmentEpoch: fence.Epoch,
		FenceRevision: uint64(fence.Revision), //nolint:gosec // G115: fence revisions are never negative.
	}
}

func fromReceipt(receipt execenv.ControlReceipt) environment.ControlReceipt {
	return environment.ControlReceipt{
		Fence: environment.Fence{
			GrantID: string(receipt.Fence.ExecutionGrantID), Epoch: receipt.Fence.EnvironmentEpoch,
			Revision: int64(receipt.Fence.FenceRevision), //nolint:gosec // G115: revisions stay below 2^53.
		},
		State: environment.ControlState(receipt.State), Confirmed: receipt.Confirmed,
	}
}

func (m *machine) Control(ctx context.Context, fence environment.Fence, state environment.ControlState) (environment.ControlReceipt, error) {
	receipt, err := m.env.Control(ctx, execenv.ControlRequest{Fence: toFence(fence), State: execenv.ControlState(state)})
	if err != nil {
		return environment.ControlReceipt{}, m.fail("control", err)
	}
	return fromReceipt(receipt), nil
}

func toContent(content *environment.ContentRef) *execenv.ContentReference {
	if content == nil {
		return nil
	}
	return &execenv.ContentReference{Size: content.Size, Digest: content.SHA256, TransferID: content.TransferID}
}

func fromContent(content *execenv.ContentReference) *environment.ContentRef {
	if content == nil {
		return nil
	}
	return &environment.ContentRef{Size: content.Size, SHA256: content.Digest, TransferID: content.TransferID}
}

func toKind(kind model.StarterWorkspaceEntryKind) execenv.ProjectionKind {
	if kind == model.StarterWorkspaceEntryDirectory {
		return execenv.ProjectionDirectory
	}
	return execenv.ProjectionFile
}

func fromKind(kind execenv.ProjectionKind) model.StarterWorkspaceEntryKind {
	if kind == execenv.ProjectionDirectory {
		return model.StarterWorkspaceEntryDirectory
	}
	return model.StarterWorkspaceEntryFile
}

func (m *machine) UploadContent(ctx context.Context, fence environment.Fence, body io.Reader) (environment.ContentRef, error) {
	reference, err := m.env.UploadProjectionContent(ctx, toFence(fence), body)
	if err != nil {
		return environment.ContentRef{}, m.fail("upload", err)
	}
	return *fromContent(&reference), nil
}

func (m *machine) InitializeProjection(ctx context.Context, request environment.ProjectionInit) (environment.ProjectionReceipt, error) {
	entries := make([]execenv.ProjectionEntry, len(request.Entries))
	for index, item := range request.Entries {
		entries[index] = execenv.ProjectionEntry{EntryID: item.EntryID.String(), Kind: toKind(item.Kind), Path: item.Path,
			ContentVersion: execenv.Version(item.Version), Content: toContent(item.Content)}
	}
	initialization := execenv.ProjectionInitialization{
		Fence: toFence(request.Fence), MutationID: request.MutationID,
		WorkspaceCursor: uint64(request.Cursor), //nolint:gosec // G115: cursors are never negative.
		Entries:         entries,
	}
	if request.IgnoreFile != "" {
		initialization.Ignore = &execenv.IgnoreRules{File: request.IgnoreFile, Fallback: request.IgnoreFallback}
	}
	receipt, err := m.env.InitializeProjection(ctx, initialization)
	if err != nil {
		return environment.ProjectionReceipt{}, m.fail("initialize", err)
	}
	return fromProjectionReceipt(receipt), nil
}

func fromProjectionReceipt(receipt execenv.ProjectionReceipt) environment.ProjectionReceipt {
	return environment.ProjectionReceipt{
		Cursor:     int64(receipt.AppliedWorkspaceCursor), //nolint:gosec // G115: cursors stay below 2^53.
		HostCursor: receipt.HostCursor,
		Discarded:  receipt.Discarded,
	}
}

func toMutation(mutation environment.ProjectionMutation) execenv.ProjectionMutation {
	return execenv.ProjectionMutation{
		JournalCursor: uint64(mutation.Cursor), //nolint:gosec // G115: cursors are never negative.
		EntryID:       mutation.EntryID.String(), Operation: execenv.ProjectionOperation(mutation.Operation),
		Kind: toKind(mutation.Kind), Path: mutation.Path, DestinationPath: mutation.DestinationPath,
		ExpectedContentVersion:  execenv.Version(mutation.ExpectedVersion),
		ResultingContentVersion: execenv.Version(mutation.ResultingVersion),
		Recursive:               mutation.Recursive, Content: toContent(mutation.Content),
		Overwrite: mutation.Overwrite,
	}
}

func (m *machine) ApplyProjection(ctx context.Context, request environment.ProjectionApply) (environment.ProjectionReceipt, error) {
	mutations := make([]execenv.ProjectionMutation, len(request.Mutations))
	for index, mutation := range request.Mutations {
		mutations[index] = toMutation(mutation)
	}
	receipt, err := m.env.ApplyProjection(ctx, execenv.ProjectionApply{
		Fence: toFence(request.Fence), MutationID: request.MutationID,
		FromWorkspaceCursor:    uint64(request.From),    //nolint:gosec // G115: cursors are never negative.
		ThroughWorkspaceCursor: uint64(request.Through), //nolint:gosec // G115: cursors are never negative.
		ExpectedHostCursor:     request.ExpectedHostCursor,
		Mutations:              mutations,
	})
	if err != nil {
		return environment.ProjectionReceipt{}, m.fail("apply", err)
	}
	return fromProjectionReceipt(receipt), nil
}

func (m *machine) Observe(ctx context.Context, request environment.ObserveRequest) (environment.ObservationPage, error) {
	page, err := m.env.Observe(ctx, execenv.ObserveRequest{Fence: toFence(request.Fence), After: request.After,
		Limit: request.Limit, Wait: request.Wait})
	if err != nil {
		return environment.ObservationPage{}, m.fail("observe", err)
	}
	result := environment.ObservationPage{HostCursor: page.HostCursor,
		Observations: make([]environment.Observation, len(page.Observations))}
	for index, observation := range page.Observations {
		result.Observations[index] = environment.Observation{
			HostSequence: observation.HostSequence, Operation: environment.ObservationOperation(observation.Operation),
			NodeIdentity: observation.NodeIdentity, Kind: fromKind(observation.Kind), Path: observation.Path,
			DestinationPath: observation.DestinationPath, ExpectedVersion: string(observation.ExpectedContentVersion),
			Recursive: observation.Recursive, Content: fromContent(observation.Content),
			ProcessTag:  model.AttemptEnvironmentProcessTag(observation.ProcessTag),
			DiscardedBy: int64(observation.DiscardedBy), //nolint:gosec // G115: cursors stay below 2^53.
		}
	}
	return result, nil
}

func (m *machine) OpenObservedContent(ctx context.Context, fence environment.Fence, content environment.ContentRef) (io.ReadCloser, error) {
	body, err := m.env.OpenObservationContent(ctx, toFence(fence), *toContent(&content))
	if err != nil {
		return nil, m.fail("open observed content", err)
	}
	return &observedBody{body: body, machine: m}, nil
}

// observedBody translates the error that ends an observed body.
type observedBody struct {
	body    io.ReadCloser
	machine *machine
}

func (b *observedBody) Read(buffer []byte) (int, error) {
	n, err := b.body.Read(buffer)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, b.machine.fail("read observed content", err)
	}
	return n, err
}

func (b *observedBody) Close() error { return b.body.Close() }

func (m *machine) ConfirmObservation(ctx context.Context, fence environment.Fence, hostSequence uint64, mutation environment.ProjectionMutation) error {
	if err := m.env.ConfirmObservationProjection(ctx, execenv.ObservationProjection{
		Fence: toFence(fence), HostSequence: hostSequence, Mutation: toMutation(mutation),
	}); err != nil {
		return m.fail("confirm", err)
	}
	return nil
}

func (m *machine) AcknowledgeObservations(ctx context.Context, fence environment.Fence, through uint64) error {
	if err := m.env.AcknowledgeObservations(ctx, toFence(fence), through); err != nil {
		return m.fail("acknowledge", err)
	}
	return nil
}

func (m *machine) ObserveProcesses(ctx context.Context, fence environment.Fence, after uint64, limit int) (environment.ProcessPage, error) {
	page, err := m.env.ObserveProcesses(ctx, toFence(fence), after, limit)
	if err != nil {
		return environment.ProcessPage{}, m.fail("observe processes", err)
	}
	result := environment.ProcessPage{Next: page.Next, Events: make([]environment.ProcessEvent, len(page.Events))}
	for index, event := range page.Events {
		result.Events[index] = environment.ProcessEvent{
			Sequence: event.Sequence, Kind: string(event.Kind), PID: event.PID, ParentPID: event.ParentPID,
			Argv: event.Argv, Truncated: event.Truncated, Unread: event.Unread, Dir: event.Dir, Tag: model.AttemptEnvironmentProcessTag(event.Tag),
			At: event.At, ExitCode: event.ExitCode, Signal: event.Signal, Dropped: event.Dropped,
		}
	}
	return result, nil
}

func (m *machine) AcknowledgeProcesses(ctx context.Context, fence environment.Fence, through uint64) error {
	if err := m.env.AcknowledgeProcesses(ctx, toFence(fence), through); err != nil {
		return m.fail("acknowledge processes", err)
	}
	return nil
}

func (m *machine) Revoke(ctx context.Context) error {
	if err := m.env.Revoke(ctx); err != nil {
		return m.fail("revoke", err)
	}
	return nil
}
