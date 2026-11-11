// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package job

import (
	"slices"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

// Descriptor is the policy one Job type runs under: its Handler, how long an
// attempt may run, how many run at once on each node, the lease and its
// heartbeat, the retry backoff, what an operator may see and do, and how long
// finished Jobs are kept. NewDescriptor fills the usual values.
type Descriptor struct {
	Type model.JobType
	// Checkpoints reports whether the handler records resumable checkpoints.
	Checkpoints           bool
	ProgressStages        []string
	PublicErrorCodes      []string
	Timeout               time.Duration
	Concurrency           int
	LeaseDuration         time.Duration
	HeartbeatInterval     time.Duration
	BaseRetryDelay        time.Duration
	MaximumRetryDelay     time.Duration
	Cancelable            bool
	ExplicitRetryStatuses []model.JobStatus
	Visibility            Visibility
	SuccessRetention      time.Duration
	FailureRetention      time.Duration
	Handler               Handler
}

// Visibility says whether operators see Jobs of a type. The engine's list,
// get, attempts, cancel and retry answer only for VisibilityOperator types
// and treat a VisibilityDomain Job as not found.
type Visibility string

// The two visibilities a Descriptor may declare.
const (
	VisibilityOperator Visibility = "operator"
	VisibilityDomain   Visibility = "domain"
)

// NewDescriptor returns the descriptor of an ordinary Job: one that runs one
// at a time and is visible to operators only. A Job
// that differs sets the fields that differ on the result, so its handler file
// shows only what is particular to it. The defaults are:
//
//	Timeout            10 minutes
//	Concurrency        1
//	LeaseDuration      1 minute
//	HeartbeatInterval  15 seconds
//	BaseRetryDelay     1 second
//	MaximumRetryDelay  1 minute
//	Visibility         VisibilityOperator
//	SuccessRetention   30 days
//	FailureRetention   90 days
//
// It records no checkpoints, declares no progress stages or public error
// codes, cannot be canceled, and cannot be retried by an operator.
func NewDescriptor(jobType model.JobType, handler Handler) Descriptor {
	return Descriptor{
		Type:              jobType,
		Timeout:           10 * time.Minute,
		Concurrency:       1,
		LeaseDuration:     time.Minute,
		HeartbeatInterval: 15 * time.Second,
		BaseRetryDelay:    time.Second,
		MaximumRetryDelay: time.Minute,
		Visibility:        VisibilityOperator,
		SuccessRetention:  30 * 24 * time.Hour,
		FailureRetention:  90 * 24 * time.Hour,
		Handler:           handler,
	}
}

// SupportsProgressStage reports whether stage is one of ProgressStages. It
// searches the sorted list NewRegistry stores, so it answers correctly only
// for a descriptor read back from a Registry or Engine.
func (d Descriptor) SupportsProgressStage(stage string) bool {
	_, ok := slices.BinarySearch(d.ProgressStages, stage)
	return ok
}

// SupportsPublicErrorCode reports whether an outcome of this type may carry
// code: no code, one of the engine's own job.* codes, or one of
// PublicErrorCodes. Like SupportsProgressStage it needs the sorted list.
func (d Descriptor) SupportsPublicErrorCode(code string) bool {
	if code == "" {
		return true
	}
	if slices.Contains([]string{"job.canceled", "job.handler_panic", "job.timeout", "job.result.invalid", "job.outcome.invalid"}, code) {
		return true
	}
	_, ok := slices.BinarySearch(d.PublicErrorCodes, code)
	return ok
}

// SupportsExplicitRetry reports whether an operator may retry a Job of this
// type that ended in status.
func (d Descriptor) SupportsExplicitRetry(status model.JobStatus) bool {
	return slices.Contains(d.ExplicitRetryStatuses, status)
}

// Registration is everything one Job contributes to the engine: the
// Descriptor that says how it runs and, when the Job is proposed on a
// schedule, the Recurrence that proposes it. Recurrence is nil for a Job
// that only other code enqueues.
type Registration struct {
	Descriptor Descriptor
	Recurrence *Recurrence
}

// SplitRegistrations returns the descriptors and the recurrences of
// registrations, each in registration order, as Dependencies takes them.
func SplitRegistrations(registrations []Registration) ([]Descriptor, []Recurrence) {
	descriptors := make([]Descriptor, 0, len(registrations))
	var recurrences []Recurrence
	for _, registration := range registrations {
		descriptors = append(descriptors, registration.Descriptor)
		if registration.Recurrence != nil {
			recurrences = append(recurrences, *registration.Recurrence)
		}
	}
	return descriptors, recurrences
}
