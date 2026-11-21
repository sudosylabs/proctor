// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPreviewPolicy(t *testing.T) {
	t.Parallel()
	ports := []PortRange{{From: 1024, To: 2048}, {From: 5173, To: 5173}, {From: 65535, To: 65535}}
	policy := DefaultExamPolicySet()
	policy.Execution = ExecutionPolicy{Enabled: true, Profile: NewEnvironmentProfileID(), Terminals: 1,
		Preview: PreviewPolicy{Enabled: true, Ports: ports}}
	encoded, err := EncodeExamPolicySet(policy)
	require.NoError(t, err)
	decoded, err := DecodeExamPolicySet(encoded)
	require.NoError(t, err)
	require.Equal(t, policy.Execution.Preview, decoded.Execution.Preview)
	for _, port := range []int{1024, 2048, 5173, 65535} {
		require.True(t, decoded.Execution.Preview.Allows(port))
	}
	for _, port := range []int{0, 1023, 2049, 5172, 65536} {
		require.False(t, decoded.Execution.Preview.Allows(port))
	}

	for _, preview := range []PreviewPolicy{
		{Ports: ports},
		{Enabled: true},
		{Enabled: true, Ports: make([]PortRange, 17)},
		{Enabled: true, Ports: []PortRange{{From: 1023, To: 1024}}},
		{Enabled: true, Ports: []PortRange{{From: 65535, To: 65536}}},
		{Enabled: true, Ports: []PortRange{{From: 5174, To: 5173}}},
		{Enabled: true, Ports: []PortRange{{From: 3000, To: 4000}, {From: 4000, To: 5000}}},
		{Enabled: true, Ports: []PortRange{{From: 5173, To: 5173}, {From: 3000, To: 3000}}},
	} {
		policy.Execution.Preview = preview
		_, err = EncodeExamPolicySet(policy)
		require.Error(t, err)
	}
	policy.Execution = ExecutionPolicy{Preview: PreviewPolicy{Enabled: true, Ports: ports}}
	_, err = EncodeExamPolicySet(policy)
	require.Error(t, err)
}

func TestDecodePreviewPolicyRefusesIncompleteOrUnknownFields(t *testing.T) {
	t.Parallel()
	canonical, err := EncodeExamPolicySet(DefaultExamPolicySet())
	require.NoError(t, err)
	const preview = `"preview":{"enabled":false,"ports":[]},`
	require.Contains(t, string(canonical), preview)
	for _, replacement := range []string{
		"",
		`"preview":null,`,
		`"preview":{"ports":[]},`,
		`"preview":{"enabled":false,"ports":null},`,
		`"preview":{"enabled":false,"enabled":false,"ports":[]},`,
		`"preview":{"enabled":false,"ports":[],"other":""},`,
		`"preview":{"enabled":true,"ports":[{"from":5173}]},`,
		`"preview":{"enabled":true,"ports":[{"from":5173,"to":5173,"other":""}]},`,
	} {
		_, err := DecodeExamPolicySet(bytes.Replace(canonical, []byte(preview), []byte(replacement), 1))
		require.Error(t, err, strings.TrimSuffix(replacement, ","))
	}
}
