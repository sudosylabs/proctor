// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSecurityControlStrictCodec(t *testing.T) {
	control := SecurityRenewal{ControlSequence: 1, PolicyDigest: SHA256Fingerprint([]byte("policy")), SecuritySessionID: "security-session", StreamID: "stream", DeliveryWatermarks: []DeliveryWatermark{}}
	raw, err := control.Canonical()
	require.NoError(t, err)
	var decoded SecurityRenewal
	require.NoError(t, json.Unmarshal(raw, &decoded))
	for _, bad := range []string{
		strings.Replace(string(raw), "\"delivery_watermarks\":[]", "\"delivery_watermarks\":null", 1),
		strings.Replace(string(raw), "\"control_sequence\":1", "\"control_sequence\":1.0", 1),
		strings.Replace(string(raw), "\"stream_id\"", "\"Stream_id\"", 1),
		strings.TrimSuffix(string(raw), "}") + `,"stream_id":"other"}`,
		strings.TrimSuffix(string(raw), "}") + `,"sources":[]}`,
	} {
		require.Error(t, json.Unmarshal([]byte(bad), &decoded), "accepted invalid control: %s", bad)
	}
	control.DeliveryWatermarks = []DeliveryWatermark{{Family: "native", SourceID: "stream", AllocatedThroughSequence: 4, AcknowledgedThroughSequence: 5}}
	require.Error(t, control.Validate(), "acknowledgement above allocation accepted")
}
