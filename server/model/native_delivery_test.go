// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNativeDeliveryClosedRecords(t *testing.T) {
	at := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	fact := NativeCapabilityFact{Kind: "capability_fact", ObservedAt: at, NativeCapabilityReadiness: readinessOf("display", NativeCertaintyComplete, `{"targets":[]}`, `{"active_physical":1}`)}
	batch := NativeSecurityBatch{StreamID: NewID(), BatchSequence: 1, ParticipationID: NewAttemptParticipationID(), Generation: 1, SecuritySessionID: NewID(), PolicyDigest: SHA256Fingerprint([]byte("policy")), ApplicationReleaseID: "release", MatrixID: "matrix", Records: []NativeRecord{{Fact: &fact}}}
	raw, err := batch.Canonical()
	require.NoError(t, err)
	var roundtrip NativeSecurityBatch
	require.NoError(t, json.Unmarshal(raw, &roundtrip))
	encoded, err := roundtrip.Canonical()
	require.NoError(t, err)
	require.True(t, bytes.Equal(raw, encoded))
	for _, mutation := range []func(string) string{
		func(s string) string {
			return strings.Replace(s, `"sequence":1,`, `"raw_inventory":"secret","sequence":1,`, 1)
		},
		func(s string) string { return strings.Replace(s, `"batch_sequence":1`, `"batch_sequence":1.0`, 1) },
		func(s string) string { return strings.Replace(s, `"generation":1`, `"generation":1,"generation":1`, 1) },
		func(s string) string { return strings.Replace(s, `"kind":"capability_fact"`, `"kind":"arbitrary"`, 1) },
		func(s string) string { return strings.Replace(s, `"sequence":1,`, `"sequence":9007199254740992,`, 1) },
		func(s string) string { return strings.Replace(s, `"state":{"targets":[]}`, `"state":[]`, 1) },
		func(s string) string { return strings.Replace(s, `"prior_acknowledgement":0,`, ``, 1) },
		func(s string) string {
			return strings.Replace(s, `"certainty":"complete"`, `"Certainty":"complete"`, 1)
		},
	} {
		var invalid NativeSecurityBatch
		require.Error(t, json.Unmarshal([]byte(mutation(string(raw))), &invalid), "accepted non-contract batch")
	}
	gap := NativeSourceGap{Kind: "source_gap", SourceID: NativeSourceCapture, SourceInstanceID: NewID(), Reason: "overflow", OccurredAt: at}
	require.Error(t, (NativeRecord{Fact: &fact, Gap: &gap}).Validate(), "accepted ambiguous union")
	batch.Records = nil
	require.Error(t, batch.Validate(), "accepted empty batch")
}

func TestUnretainedDeliverySummaryPreservesUncertainty(t *testing.T) {
	prior := UnretainedDeliverySummary{SummarySequence: 1, UnretainedRecordCount: 7, CountComplete: false}
	replay, err := prior.Compare(prior)
	require.True(t, replay, "replay")
	require.NoError(t, err, "replay")
	for _, next := range []UnretainedDeliverySummary{
		{SummarySequence: 2, UnretainedRecordCount: 6, CountComplete: false},
		{SummarySequence: 2, UnretainedRecordCount: 8, CountComplete: true},
		{SummarySequence: 1, UnretainedRecordCount: 8, CountComplete: false},
	} {
		_, err = prior.Compare(next)
		require.Error(t, err, "lost uncertainty or accepted conflicting summary")
	}
	var decoded UnretainedDeliverySummary
	require.NoError(t, json.Unmarshal([]byte(`{"summary_sequence":1,"unretained_record_count":0,"count_complete":false}`), &decoded), "rejected omitted absent instants")
	require.Nil(t, decoded.FirstUnretainedAt)
	require.Error(t, json.Unmarshal([]byte(`{"summary_sequence":1,"unretained_record_count":0,"count_complete":false,"first_unretained_at":null}`), &decoded), "accepted an explicit null instant")
}
