// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import "fmt"

const (
	ExamResourceDefaultMaximumCount       = 10
	ExamResourceDefaultMaximumBytes int64 = 10 << 20
	ExamResourceMaximumCount              = 100
	ExamResourceMaximumBytes        int64 = 100 << 20

	ExamWorkspaceDefaultMaximumEntries          = 500
	ExamWorkspaceDefaultMaximumFileBytes  int64 = 10 << 20
	ExamWorkspaceDefaultMaximumTotalBytes int64 = 50 << 20
	ExamWorkspaceMaximumEntries                 = 5000
	ExamWorkspaceMaximumFileBytes         int64 = 100 << 20
	ExamWorkspaceMaximumTotalBytes        int64 = 1 << 30
)

// ExamCapacityPolicy is the institution's admission policy for authored exam
// content and candidate workspace growth. Published revisions freeze it, so a
// later change cannot alter a sitting in progress. The constants above are
// fixed server ceilings, not institution policy.
type ExamCapacityPolicy struct {
	ResourceMaximumCount       int   `json:"resource_maximum_count"`
	ResourceMaximumBytes       int64 `json:"resource_maximum_bytes"`
	WorkspaceMaximumEntries    int   `json:"workspace_maximum_entries"`
	WorkspaceMaximumFileBytes  int64 `json:"workspace_maximum_file_bytes"`
	WorkspaceMaximumTotalBytes int64 `json:"workspace_maximum_total_bytes"`
}

func DefaultExamCapacityPolicy() ExamCapacityPolicy {
	return ExamCapacityPolicy{
		ResourceMaximumCount:       ExamResourceDefaultMaximumCount,
		ResourceMaximumBytes:       ExamResourceDefaultMaximumBytes,
		WorkspaceMaximumEntries:    ExamWorkspaceDefaultMaximumEntries,
		WorkspaceMaximumFileBytes:  ExamWorkspaceDefaultMaximumFileBytes,
		WorkspaceMaximumTotalBytes: ExamWorkspaceDefaultMaximumTotalBytes,
	}
}

func (p ExamCapacityPolicy) IsZero() bool { return p == (ExamCapacityPolicy{}) }

func (p ExamCapacityPolicy) Validate() error {
	if p.ResourceMaximumCount < 1 || p.ResourceMaximumCount > ExamResourceMaximumCount {
		return invalidModelError(
			"ExamCapacityPolicy.Validate",
			"exam_capacity_policy",
			"",
			fmt.Sprintf("Exam resource maximum count must be between 1 and %d", ExamResourceMaximumCount),
			"",
		)
	}

	if p.ResourceMaximumBytes < 1 || p.ResourceMaximumBytes > ExamResourceMaximumBytes {
		return invalidModelError(
			"ExamCapacityPolicy.Validate",
			"exam_capacity_policy",
			"",
			fmt.Sprintf("Exam resource maximum bytes must be between 1 and %d", ExamResourceMaximumBytes),
			"",
		)
	}

	if p.WorkspaceMaximumEntries < 1 || p.WorkspaceMaximumEntries > ExamWorkspaceMaximumEntries {
		return invalidModelError(
			"ExamCapacityPolicy.Validate",
			"exam_capacity_policy",
			"",
			fmt.Sprintf("Exam Workspace maximum entries must be between 1 and %d", ExamWorkspaceMaximumEntries),
			"",
		)
	}

	if p.WorkspaceMaximumFileBytes < 1 || p.WorkspaceMaximumFileBytes > ExamWorkspaceMaximumFileBytes {
		return invalidModelError(
			"ExamCapacityPolicy.Validate",
			"exam_capacity_policy",
			"",
			fmt.Sprintf("Exam Workspace file maximum bytes must be between 1 and %d", ExamWorkspaceMaximumFileBytes),
			"",
		)
	}

	if p.WorkspaceMaximumTotalBytes < p.WorkspaceMaximumFileBytes || p.WorkspaceMaximumTotalBytes > ExamWorkspaceMaximumTotalBytes {
		return invalidModelError(
			"ExamCapacityPolicy.Validate",
			"exam_capacity_policy",
			"",
			fmt.Sprintf("Exam Workspace total maximum bytes must be between the file limit and %d", ExamWorkspaceMaximumTotalBytes),
			"",
		)
	}

	return nil
}
