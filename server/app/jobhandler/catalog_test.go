// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package jobhandler

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/model"
)

// testDescriptors builds every Job descriptor through its exported
// constructor with no collaborators, in the order the application registers
// them. The tests that use it read descriptor policy and never run a handler.
// onboarding, when non-nil, adds the onboarding import Jobs the application registers
// only when that service exists.
func testDescriptors(onboarding OnboardingImportService) []job.Descriptor {
	descriptors := []job.Descriptor{
		NewDefaultProfilePictureDescriptor(nil),
		NewDefaultProfilePictureReconciliationDescriptor(nil, nil, nil),
		NewFilePurgeDescriptor(FilePurgeDependencies{}),
		NewCommandOutcomeCleanupDescriptor(nil),
		NewAuditAttemptSweepDescriptor(nil),
		NewSessionPurgeDescriptor(nil, nil, 0),
		NewMailDeliveryDescriptor(MailDeliveryDependencies{}),
		NewMailCredentialDeliveryDescriptor(MailDeliveryDependencies{}),
		NewSittingMailExpansionDescriptor(nil, nil),
		NewMailCleanupDescriptor(nil, nil, nil),
		NewMailRekeyDescriptor(nil, nil),
		NewMFARekeyDescriptor(nil, nil),
		NewInvitationMaintenanceDescriptor(nil, nil, nil, nil),
		NewExamSittingLifecycleDescriptor(nil),
		NewExamSittingSealingDescriptor(nil),
		NewExamSittingLifecycleRecoveryDescriptor(nil),
		NewExamExportBuildDescriptor(nil),
		NewExamExportCleanupDescriptor(nil, nil),
		NewRetentionExpiryDescriptor(nil, nil, nil, nil),
		NewRetentionReconcileDescriptor(nil, nil, nil, nil),
		NewRetentionPurgeDescriptor(nil, nil, nil, nil),
		NewRetentionNoticeDescriptor(nil, nil, nil, nil),
	}
	if onboarding != nil {
		descriptors = append(descriptors, NewOnboardingImportDescriptors(onboarding)...)
	}
	return append(descriptors, NewJobHistoryCleanupDescriptor(nil, nil, nil, descriptors))
}

// testRecurrences builds every Recurrence through its exported constructor.
func testRecurrences() []job.Recurrence {
	return []job.Recurrence{
		NewDefaultProfilePictureReconciliationRecurrence(nil, nil),
		NewFilePurgeRecurrence(nil, nil),
		NewJobHistoryCleanupRecurrence(nil, nil),
		NewCommandOutcomeCleanupRecurrence(nil, nil),
		NewAuditAttemptSweepRecurrence(nil, nil),
		NewSessionPurgeRecurrence(nil, nil),
		NewMailCleanupRecurrence(nil, nil),
		NewInvitationMaintenanceRecurrence(nil, nil),
		NewExamSittingLifecycleRecoveryRecurrence(nil, nil),
		NewExamExportCleanupRecurrence(nil, nil),
		NewRetentionExpiryRecurrence(nil, nil),
		NewRetentionReconcileRecurrence(nil, nil),
		NewRetentionPurgeRecurrence(nil, nil),
		NewRetentionNoticeRecurrence(nil, nil),
	}
}

func TestCatalogDeclaresTheCompleteCoreDurableWorkGraph(t *testing.T) {
	t.Parallel()
	descriptors, recurrences := testDescriptors(nil), testRecurrences()
	_, err := job.NewRegistry(descriptors)
	require.NoError(t, err)
	wantTypes := map[model.JobType]bool{
		model.JobTypeProfilePictureGenerateDefault: true,
		model.JobTypeProfilePictureReconcile:       true,
		model.JobTypeFilePurgeExpiredContent:       true,
		model.JobTypeCommandOutcomeCleanup:         true,
		model.JobTypeAuditAttemptSweep:             true,
		model.JobTypeSessionPurgeRetired:           true,
		model.JobTypeMailDeliver:                   true,
		model.JobTypeMailDeliverCredential:         true,
		model.JobTypeMailExpandSitting:             true,
		model.JobTypeMailCleanup:                   true,
		model.JobTypeMailRekey:                     true,
		model.JobTypeMFARekey:                      true,
		model.JobTypeInvitationMaintenance:         true,
		model.JobTypeExamSittingLifecycle:          true,
		model.JobTypeExamSittingSealing:            true,
		model.JobTypeExamSittingLifecycleRecovery:  true,
		model.JobTypeExamExportBuild:               true,
		model.JobTypeExamExportCleanup:             true,
		model.JobTypeRetentionExpire:               true,
		model.JobTypeRetentionReconcile:            true,
		model.JobTypeRetentionPurge:                true,
		model.JobTypeRetentionNotices:              true,
		model.JobTypeCleanup:                       true,
	}
	require.Len(t, descriptors, len(wantTypes), "descriptor count")
	for _, descriptor := range descriptors {
		require.True(t, wantTypes[descriptor.Type], "unexpected descriptor %q", descriptor.Type)
	}
	wantRecurrences := map[string]bool{
		"profile-picture-default-reconciliation": true,
		"file-purge-expired-content":             true,
		"job-history-cleanup":                    true,
		"command-outcome-cleanup":                true,
		"audit-attempt-sweep":                    true,
		"session-purge-retired":                  true,
		"mail-cleanup":                           true,
		"invitation-maintenance":                 true,
		"exam-sitting-lifecycle-recovery":        true,
		"exam-export-cleanup":                    true,
		"retention-expire":                       true,
		"retention-reconcile":                    true,
		"retention-purge":                        true,
		"retention-notices":                      true,
	}
	require.Len(t, recurrences, len(wantRecurrences), "recurrence count")
	for _, recurrence := range recurrences {
		require.True(t, wantRecurrences[recurrence.Name], "unexpected recurrence %q", recurrence.Name)
	}
}
