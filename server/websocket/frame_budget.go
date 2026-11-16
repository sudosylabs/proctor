// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package websocket

import (
	"sync"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

const (
	// inboundFrameBurst and inboundFramesPerSecond bound the request frames
	// one session (or, without one, one user) may send to this node across
	// all its connections. Legitimate clients stay well below the sustained
	// rate.
	inboundFrameBurst      = 64
	inboundFramesPerSecond = 16
	// inboundRefusalsBeforeClose closes a connection that keeps sending after
	// its budget is exhausted instead of answering each refused frame forever.
	inboundRefusalsBeforeClose = 64
	// frameBudgetIdleRetention is how long a budget outlives its last
	// connection. After it a released budget would have refilled to the full
	// burst, so forgetting it changes nothing; before it, reconnecting does
	// not reset a spent budget.
	frameBudgetIdleRetention = time.Duration(inboundFrameBurst/inboundFramesPerSecond) * time.Second
)

// frameBudget is a token bucket over inbound request frames. The zero value
// starts with a full burst on first use. It is not safe for concurrent use.
type frameBudget struct {
	tokens      float64
	updated     time.Time
	initialized bool
}

func (b *frameBudget) allow(now time.Time) bool {
	if !b.initialized {
		b.tokens, b.updated, b.initialized = inboundFrameBurst, now, true
	}
	if elapsed := now.Sub(b.updated); elapsed > 0 {
		b.tokens = min(float64(inboundFrameBurst), b.tokens+elapsed.Seconds()*inboundFramesPerSecond)
		b.updated = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// sharedFrameBudget is shared by all connections of one session or user. The
// hub shard lock guards holders and releasedAt.
type sharedFrameBudget struct {
	mu         sync.Mutex
	bucket     frameBudget
	holders    int
	releasedAt time.Time
}

func (s *sharedFrameBudget) allow(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bucket.allow(now)
}

func frameBudgetKey(principal model.Principal) string {
	if !principal.SessionID.IsZero() {
		return "session:" + principal.SessionID.String()
	}
	return "user:" + principal.UserID.String()
}

// inboundFrameGate is owned by the read pump. Without a hub it uses a private
// budget.
type inboundFrameGate struct {
	shared   *sharedFrameBudget
	refusals int
}

func (g *inboundFrameGate) allow(now time.Time) bool {
	if g.shared == nil {
		g.shared = &sharedFrameBudget{}
	}
	if g.shared.allow(now) {
		g.refusals = 0
		return true
	}
	g.refusals++
	return false
}

// exhausted reports whether consecutive refusals warrant closing the
// connection.
func (g *inboundFrameGate) exhausted() bool {
	return g.refusals >= inboundRefusalsBeforeClose
}
