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
	// ExamAttemptExpiryTaskName names the task that settles expired
	// participations and abandoned deliveries; its scans record it as their
	// system caller.
	ExamAttemptExpiryTaskName = "exam-attempt-participation-expiry"
	// ExamAttemptExpiryScanInterval is the pause between two scans on a node.
	ExamAttemptExpiryScanInterval = 2 * time.Second
	examAttemptExpiryBatchLimit   = 200
)

// ExamAttemptExpiryRunner runs the attempt expiry scans as a non-durable
// periodic task. Cross-node claiming and idempotency stay in the store
// operation.
type ExamAttemptExpiryRunner struct{ Attempts *attempt.Service }

// Run scans expired participations and then abandoned deliveries, one batch
// each. A failed participation scan ends the pass.
func (r ExamAttemptExpiryRunner) Run(ctx context.Context) error {
	system := invocation.Task(ExamAttemptExpiryTaskName)
	_, err := r.Attempts.ScanExpiredParticipations(ctx, system, examAttemptExpiryBatchLimit)
	if err != nil {
		return err
	}
	_, err = r.Attempts.ScanExpiredDeliveries(ctx, system, examAttemptExpiryBatchLimit)
	return err
}

// NewExamAttemptExpiryTask runs the attempt expiry scans on every node.
func NewExamAttemptExpiryTask(attempts *attempt.Service) job.PeriodicTask {
	return job.PeriodicTask{
		Name:     ExamAttemptExpiryTaskName,
		Interval: ExamAttemptExpiryScanInterval,
		Runner:   ExamAttemptExpiryRunner{Attempts: attempts},
	}
}
