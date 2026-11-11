// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestProtectedPresentationUsesCurrentRevisionAndSanitizesCandidateMarkdown(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	credential := model.NewCredentialToken()
	currentID := model.NewExamRevisionID()
	f.persistence.presentation = &store.CandidateExamPresentation{
		Capacity:  model.DefaultExamCapacityPolicy(),
		AttemptID: f.attemptID, SittingID: f.sitting.ID,
		RuntimeCapabilities: runtimeCapabilitiesFixture(f, currentID), BrowserPolicy: disabledCandidateBrowserPolicyFixture(currentID),
		Title: "Algorithms", InstructionsMarkdown: "# Rules\nUse **Go**.\n<script>alert('x')</script>\n[bad](javascript:alert(1))\n![tracker](https://example.test/pixel.png)\n[handbook](https://example.test/handbook)",
		Resources: []store.CandidateExamResource{{ResourceID: model.NewExamResourceID(), DisplayName: "Reference",
			DescriptionMarkdown: "Read _carefully_. <iframe src=https://evil.test></iframe> [data](data:text/html,bad)", Position: 0, MediaType: model.ExamResourceMediaText, SizeBytes: 4, SHA256: strings.Repeat("a", 64)}},
	}
	result, err := f.service.GetCandidateExamPresentation(context.Background(), f.invocation, CandidateAccess{
		AttemptID: f.attemptID, ConnectionID: f.connectionID, ContinuityCredential: credential,
	})
	require.NoError(t, err)
	require.Equal(t, f.persistence.presentation.Capacity, result.Capacity)
	require.Equal(t, f.revision.ID, result.RuntimeCapabilities.ExamRevision.AdmissionRevisionID)
	require.Equal(t, currentID, result.RuntimeCapabilities.ExamRevision.CurrentRevisionID)
	require.True(t, result.RuntimeCapabilities.FocusLossCollectionEnabled)
	require.Contains(t, result.InstructionsMarkdown, "# Rules")
	require.Contains(t, result.InstructionsMarkdown, "**Go**")
	require.Contains(t, result.InstructionsMarkdown, "[handbook](https://example.test/handbook)")
	require.Contains(t, result.Resources[0].DescriptionMarkdown, "_carefully_")
	for _, forbidden := range []string{"<script", "alert('x')", "javascript:", "![tracker]", "pixel.png", "<iframe", "evil.test", "data:text/html"} {
		require.NotContains(t, result.InstructionsMarkdown+result.Resources[0].DescriptionMarkdown, forbidden, "unsafe Markdown survived %q: %#v", forbidden, result)
	}
	require.Equal(t, model.HashToken(credential), f.persistence.candidateAccess.ContinuityCredentialHash)
	require.Equal(t, f.userID, f.persistence.candidateAccess.CandidateUserID)
	require.Equal(t, f.invocation.Principal().SessionID, f.persistence.candidateAccess.SessionID)
}

func TestProtectedPresentationRejectsInvalidFrozenCapacity(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		capacity model.ExamCapacityPolicy
	}{
		{name: "missing"},
		{name: "total below file limit", capacity: model.ExamCapacityPolicy{ResourceMaximumCount: 1, ResourceMaximumBytes: 1, WorkspaceMaximumEntries: 1, WorkspaceMaximumFileBytes: 2, WorkspaceMaximumTotalBytes: 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			f.persistence.presentation = &store.CandidateExamPresentation{
				AttemptID: f.attemptID, SittingID: f.sitting.ID, Capacity: test.capacity,
				RuntimeCapabilities: runtimeCapabilitiesFixture(f, f.revision.ID), BrowserPolicy: disabledCandidateBrowserPolicyFixture(f.revision.ID),
			}
			_, err := f.service.GetCandidateExamPresentation(context.Background(), f.invocation, CandidateAccess{AttemptID: f.attemptID, ConnectionID: f.connectionID, ContinuityCredential: model.NewCredentialToken()})
			var fault *apperror.Error
			require.ErrorAs(t, err, &fault)
			require.Equal(t, "exam.attempt.unavailable", fault.Code())
		})
	}
}

func TestProtectedPresentationRejectsAConnectionFromAnotherSession(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.persistence.authorizedSession = f.invocation.Principal().SessionID
	principal := f.invocation.Principal()
	principal.SessionID = model.NewSessionID()
	otherSession := invocation.New(principal, f.invocation.RequestMetadata())
	_, err := f.service.GetCandidateExamPresentation(context.Background(), otherSession, CandidateAccess{
		AttemptID: f.attemptID, ConnectionID: f.connectionID, ContinuityCredential: model.NewCredentialToken(),
	})
	require.True(t, apperror.Is(err, "resource.not_found"), "error=%v access=%#v", err, f.persistence.candidateAccess)
	require.True(t, chainHasCode(err, "exam.attempt.not_found"), "error=%v access=%#v", err, f.persistence.candidateAccess)
	require.Equal(t, principal.SessionID, f.persistence.candidateAccess.SessionID, "error=%v access=%#v", err, f.persistence.candidateAccess)
}
