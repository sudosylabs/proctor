// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package mail

import (
	"sync/atomic"
)

// The mail health codes. Mail is disabled by configuration, healthy, or
// degraded by an SMTP outage or a delayed queue.
const (
	HealthDisabled     = "mail.disabled"
	HealthHealthy      = "mail.healthy"
	HealthSMTPOutage   = "mail.smtp_outage"
	HealthQueueDelayed = "mail.queue_delayed"
)

// Health is the current mail health code of this node. The delivery Jobs and
// the MaintenanceMonitor set it; Access Policy capabilities and the metrics
// read it.
type Health struct{ code atomic.Value }

// NewHealth starts healthy when mail is enabled and disabled otherwise.
func NewHealth(enabled bool) *Health {
	health := &Health{}
	if enabled {
		health.code.Store(HealthHealthy)
	} else {
		health.code.Store(HealthDisabled)
	}
	return health
}

// Code returns the current health code; a nil Health is disabled.
func (h *Health) Code() string {
	if h == nil {
		return HealthDisabled
	}
	if code, ok := h.code.Load().(string); ok {
		return code
	}
	return HealthDisabled
}

// Degraded reports an SMTP outage or a delayed queue.
func (h *Health) Degraded() bool {
	code := h.Code()
	return code != HealthHealthy && code != HealthDisabled
}

// Set records code and reports whether it changed the health.
func (h *Health) Set(code string) bool {
	if h != nil {
		previous := h.code.Swap(code)
		return previous != code
	}
	return false
}
