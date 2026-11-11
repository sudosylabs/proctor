// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package jobs

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/model"
)

// testRegistrations builds every Job through its exported constructor with
// no collaborators, in the order the application registers them. The tests
// that use it read descriptor and recurrence policy and never run a handler.
// onboarding, when non-nil, adds the onboarding import Jobs the application
// registers only when that service exists.
func testRegistrations(onboarding OnboardingImportService) []job.Registration {
	registrations := []job.Registration{
		NewDefaultProfilePicture(nil),
		NewDefaultProfilePictureReconciliation(nil, nil, nil, nil),
		NewFilePurge(FilePurgeDependencies{}),
		NewCommandOutcomeCleanup(nil, nil, nil),
		NewAuditAttemptSweep(nil, nil, nil),
		NewSessionPurge(nil, nil, 0, nil, nil),
		NewMailDelivery(MailDeliveryDependencies{}),
		NewMailCredentialDelivery(MailDeliveryDependencies{}),
		NewSittingMailExpansion(nil, nil),
		NewMailCleanup(nil, nil, nil, nil, nil),
		NewMailRekey(nil, nil),
		NewMFARekey(nil, nil),
		NewInvitationMaintenance(nil, nil, nil, nil, nil),
		NewExamSittingLifecycle(nil),
		NewExamSittingSealing(nil, nil, nil, nil, nil),
		NewExamSittingLifecycleRecovery(nil, nil, nil),
		NewExamExportBuild(nil),
		NewExamExportCleanup(nil, nil, nil, nil),
		NewRetentionExpiry(nil, nil, nil, nil),
		NewRetentionReconcile(nil, nil, nil, nil),
		NewRetentionPurge(nil, nil, nil, nil),
		NewRetentionNotice(nil, nil, nil, nil),
	}
	if onboarding != nil {
		registrations = append(registrations,
			NewOnboardingImportParse(onboarding),
			NewStudentProgressionPreview(onboarding),
			NewOnboardingImportExecute(onboarding),
		)
	}
	descriptors, _ := job.SplitRegistrations(registrations)
	return append(registrations, NewJobHistoryCleanup(nil, nil, nil, descriptors))
}

// testDescriptors is the descriptor of every Job in testRegistrations.
func testDescriptors(onboarding OnboardingImportService) []job.Descriptor {
	descriptors, _ := job.SplitRegistrations(testRegistrations(onboarding))
	return descriptors
}

// testRecurrences is the recurrence of every scheduled Job in
// testRegistrations.
func testRecurrences() []job.Recurrence {
	_, recurrences := job.SplitRegistrations(testRegistrations(nil))
	return recurrences
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
