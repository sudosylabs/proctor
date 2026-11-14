// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package plugin_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/plugin"
)

func TestExamAttemptSubmittedJSONNamesAreStable(t *testing.T) {
	data, err := json.Marshal(plugin.ExamAttemptSubmitted{
		SubmissionID: "s", AttemptID: "a", SittingID: "t", ExamID: "e",
	})
	require.NoError(t, err)
	require.JSONEq(t, `{"submission_id":"s","attempt_id":"a","sitting_id":"t","exam_id":"e"}`, string(data))
	require.Equal(t, "exam.attempt.submitted", string(plugin.TopicExamAttemptSubmitted))
}
