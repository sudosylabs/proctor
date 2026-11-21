// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package environment

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// MaxPreviewConnections bounds a single node's open connections per attempt.
// The execution host also enforces a per-grant limit across nodes.
const MaxPreviewConnections = 32

var (
	// ErrPreviewNotAllowed means the frozen policy does not include the port.
	ErrPreviewNotAllowed = errors.New("the Exam does not allow that preview port")
	// ErrPreviewLimit covers both the node and the grant limit.
	ErrPreviewLimit = errors.New("the preview connection limit is reached")
)

// Previews serves candidates using the same access and machine fence as
// terminals. The connection count is local and includes pending dials.
type Previews struct {
	terminals *Terminals
	mu        sync.Mutex
	open      map[model.ExamAttemptID]int
}

// NewPreviews shares the terminal authorizer and execution host.
func NewPreviews(terminals *Terminals) *Previews {
	if terminals == nil {
		return nil
	}
	return &Previews{terminals: terminals, open: make(map[model.ExamAttemptID]int)}
}

// PreviewPort describes a listener and whether the frozen policy permits it.
type PreviewPort struct {
	ListeningPort
	Allowed bool
}

// ListPreviewPorts reads the candidate's listeners on the current grant.
func (p *Previews) ListPreviewPorts(ctx context.Context, inv invocation.Invocation, access CandidateAccess) ([]PreviewPort, error) {
	candidate, err := p.verify(ctx, inv, access)
	if err != nil {
		return nil, err
	}
	at, err := p.terminals.reach(ctx, candidate.AttemptID)
	if err != nil {
		return nil, previewEnvironmentFailure(err)
	}
	if at.row.Generation != candidate.Generation {
		return nil, previewEnvironmentFailure(ErrEnvironmentNotRunning)
	}
	listed, err := at.machine.ListPorts(ctx)
	if err != nil {
		return nil, previewEnvironmentFailure(err)
	}
	ports := make([]PreviewPort, len(listed))
	for i, port := range listed {
		ports[i] = PreviewPort{ListeningPort: port, Allowed: at.row.Preview.Allows(port.Port)}
	}
	return ports, nil
}

// OpenPreview checks the frozen policy before dialing under the control fence.
func (p *Previews) OpenPreview(ctx context.Context, inv invocation.Invocation, access CandidateAccess, port int) (*PreviewConn, error) {
	candidate, err := p.verify(ctx, inv, access)
	if err != nil {
		return nil, err
	}
	at, err := p.terminals.reach(ctx, candidate.AttemptID)
	if err != nil {
		return nil, previewFailure(err)
	}
	if at.row.Generation != candidate.Generation {
		return nil, previewFailure(ErrEnvironmentNotRunning)
	}
	if !at.row.Preview.Allows(port) {
		return nil, previewFailure(ErrPreviewNotAllowed)
	}
	attemptID := candidate.AttemptID
	if !p.reserve(attemptID) {
		return nil, previewFailure(ErrPreviewLimit)
	}
	var conn PortConn
	err = p.terminals.fenced(ctx, &at, func(fence Fence) error {
		if at.row.Generation != candidate.Generation {
			return ErrEnvironmentNotRunning
		}
		var dialErr error
		conn, dialErr = at.machine.ConnectPort(ctx, fence, port)
		return dialErr
	})
	if err != nil {
		p.release(attemptID)
		return nil, previewFailure(err)
	}
	return &PreviewConn{conn: conn, previews: p, at: at, inv: inv, access: access, port: port}, nil
}

func (p *Previews) verify(ctx context.Context, inv invocation.Invocation, access CandidateAccess) (CandidateAttempt, error) {
	if p == nil {
		return CandidateAttempt{}, apperror.New("exam.attempt.environment_unavailable")
	}
	return p.terminals.verify(ctx, inv, access)
}

func (p *Previews) reserve(attemptID model.ExamAttemptID) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.open[attemptID] >= MaxPreviewConnections {
		return false
	}
	p.open[attemptID]++
	return true
}

func (p *Previews) release(attemptID model.ExamAttemptID) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.open[attemptID]--
	if p.open[attemptID] == 0 {
		delete(p.open, attemptID)
	}
}

// PreviewConn is the candidate's connection to one port on one incarnation.
type PreviewConn struct {
	conn     PortConn
	previews *Previews
	at       reached
	inv      invocation.Invocation
	access   CandidateAccess
	once     sync.Once
	closeErr error
	port     int
	// Close first interrupts the connection, then waits for in-flight
	// reads and writes to count the bytes they transferred.
	ioMu     sync.RWMutex
	bytesIn  atomic.Int64
	bytesOut atomic.Int64
}

// Read transfers bytes out of the guest to the candidate.
func (c *PreviewConn) Read(p []byte) (int, error) {
	c.ioMu.RLock()
	defer c.ioMu.RUnlock()
	n, err := c.conn.Read(p)
	c.bytesOut.Add(int64(n))
	return n, err
}

// Write transfers bytes in from the candidate to the guest.
func (c *PreviewConn) Write(p []byte) (int, error) {
	c.ioMu.RLock()
	defer c.ioMu.RUnlock()
	n, err := c.conn.Write(p)
	c.bytesIn.Add(int64(n))
	return n, err
}

// CloseWrite forwards EOF to the guest while leaving its response readable.
func (c *PreviewConn) CloseWrite() error { return c.conn.CloseWrite() }

// Close interrupts both directions and returns the node's connection slot.
func (c *PreviewConn) Close() error {
	c.once.Do(func() {
		c.closeErr = c.conn.Close()
		attemptID := c.at.row.AttemptID
		c.previews.release(attemptID)
		c.ioMu.Lock()
		bytesIn, bytesOut := c.bytesIn.Load(), c.bytesOut.Load()
		c.ioMu.Unlock()
		deps := c.previews.terminals.deps
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err := deps.Environments.AddPreviewUsage(ctx, attemptID, c.port, bytesIn, bytesOut, deps.Now())
		if err != nil && !store.IsConflict(err) && deps.Report != nil {
			deps.Report(attemptID, err)
		}
	})
	return c.closeErr
}

// Check ends the stream if live access or the original running grant is gone.
func (c *PreviewConn) Check(ctx context.Context) error {
	t := c.previews.terminals
	if _, err := t.verify(ctx, c.inv, c.access); err != nil {
		return ErrTerminalAccessEnded
	}
	row, err := t.deps.Environments.Get(ctx, c.at.row.AttemptID)
	if err != nil || row.Desired != model.AttemptEnvironmentDesiredRunning || row.State != model.AttemptEnvironmentRunning ||
		row.GrantID != c.at.row.GrantID || row.Epoch != c.at.row.Epoch || row.Generation != c.at.row.Generation {
		return ErrTerminalAccessEnded
	}
	return nil
}

// PreviewAccessEnded recognizes host-initiated stream termination before
// the next candidate access check.
func PreviewAccessEnded(err error) bool {
	return errors.Is(err, ErrMachineFrozen) || errors.Is(err, ErrMachineRevoked) || errors.Is(err, ErrMachineLost)
}

func previewFailure(err error) error {
	switch {
	case errors.Is(err, ErrPreviewNotAllowed):
		return apperror.New("exam.attempt.preview_not_allowed").Wrap(err)
	case errors.Is(err, ErrPreviewLimit), errors.Is(err, ErrMachineBusy):
		return apperror.New("exam.attempt.preview_limit_reached").Wrap(ErrPreviewLimit)
	case errors.Is(err, ErrPortRefused):
		return apperror.New("exam.attempt.preview_port_closed").Wrap(err)
	default:
		return previewEnvironmentFailure(err)
	}
}

func previewEnvironmentFailure(err error) error {
	switch {
	case errors.Is(err, ErrNoEnvironment):
		return apperror.New("exam.attempt.environment_not_found").Wrap(err)
	case errors.Is(err, ErrEnvironmentNotRunning), errors.Is(err, ErrTerminalEnded), PreviewAccessEnded(err):
		return apperror.New("exam.attempt.environment_not_running").Wrap(err)
	case errors.Is(err, context.Canceled):
		return err
	default:
		return apperror.New("exam.attempt.environment_unavailable").Wrap(err)
	}
}
