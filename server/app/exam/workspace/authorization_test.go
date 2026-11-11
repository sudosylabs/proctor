// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package workspace

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServiceStopsWhenCurrentManagerMembershipCannotBeRead(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	failure := errors.New("membership unavailable")
	fixture.service.academicUnitMembers = fakeMemberships{err: failure}
	_, err := fixture.service.CreateExamStarterWorkspaceDirectory(context.Background(), fixture.inv, CreateDirectoryCommand{
		ExamID: fixture.examID, ExpectedDraftRevision: 1, Path: "src", IdempotencyKey: "test-key",
	})
	require.Equal(t, "exam.starter_workspace.unavailable", faultCode(err))
	require.ErrorIs(t, err, failure)
	require.False(t, fixture.authorizer.called, "membership failure continued: authorizer=%#v audit=%#v mutation", fixture.authorizer, fixture.auditor)
	require.Nil(t, fixture.auditor.values, "membership failure continued: authorizer=%#v audit=%#v mutation", fixture.authorizer, fixture.auditor)
	require.Nil(t, fixture.persistence.mutation, "membership failure continued: authorizer=%#v audit=%#v mutation", fixture.authorizer, fixture.auditor)
}
