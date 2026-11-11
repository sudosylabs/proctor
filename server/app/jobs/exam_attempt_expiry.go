// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
package jobs

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/job"
)

const (
	// ExamAttemptExpiryTaskName names the runtime task that settles expired
	// Participations and abandoned deliveries; its scans record it as their
	// system caller.
	ExamAttemptExpiryTaskName = "exam-attempt-participation-expiry"
	// ExamAttemptExpiryScanInterval is the pause between two scans on a node.
	ExamAttemptExpiryScanInterval = 2 * time.Second
	examAttemptExpiryBatchLimit   = 200
)

// ExamAttemptExpiryScanner is the pair of bounded Attempt scans the runtime
// task runs. *attempt.Service satisfies it and tests substitute a fake.
type ExamAttemptExpiryScanner interface {
	ScanExpiredParticipations(context.Context, invocation.System, int) (attempt.ExpiryScanResult, error)
	ScanExpiredDeliveries(context.Context, invocation.System, int) (attempt.ExpiryScanResult, error)
}

// ExamAttemptExpiryRunner adapts the bounded application scan to the
// generic non-durable runtime loop. Cross-node claiming and idempotency stay in
// the named Store operation; this adapter creates no durable Job occurrence.
type ExamAttemptExpiryRunner struct{ Attempts ExamAttemptExpiryScanner }

// Run scans expired Participations and then abandoned deliveries, each up to
// one batch. A failed Participation scan ends the pass.
func (r ExamAttemptExpiryRunner) Run(ctx context.Context) error {
	system := invocation.Task(ExamAttemptExpiryTaskName)
	_, err := r.Attempts.ScanExpiredParticipations(ctx, system, examAttemptExpiryBatchLimit)
	if err != nil {
		return err
	}
	_, err = r.Attempts.ScanExpiredDeliveries(ctx, system, examAttemptExpiryBatchLimit)
	return err
}

// NewExamAttemptExpiryTask is the periodic runtime task that runs the Attempt
// expiry scans on every node.
func NewExamAttemptExpiryTask(attempts ExamAttemptExpiryScanner) job.PeriodicTask {
	return job.PeriodicTask{
		Name:     ExamAttemptExpiryTaskName,
		Interval: ExamAttemptExpiryScanInterval,
		Runner:   ExamAttemptExpiryRunner{Attempts: attempts},
	}
}
