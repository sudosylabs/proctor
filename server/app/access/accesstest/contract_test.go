// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package accesstest

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// recordingT captures a failure instead of ending the test.
type recordingT struct {
	testing.TB
	failed bool
}

func (r *recordingT) Errorf(string, ...any) { r.failed = true }
func (r *recordingT) FailNow()              { panic(r) }
func (r *recordingT) Helper()               {}

func contractHolds(t *testing.T, events, lookups []string, want ...string) (held bool) {
	t.Helper()
	recorder := &recordingT{TB: t}
	defer func() {
		if recovered := recover(); recovered != nil && recovered != recorder {
			panic(fmt.Sprint(recovered))
		}
		held = !recorder.failed
	}()
	RequireContract(recorder, events, lookups, want...)
	return true
}

func TestRequireContractIgnoresLookupsAndKeepsEveryWrite(t *testing.T) {
	t.Parallel()
	lookups := []string{"get-class", "get-user"}
	want := []string{"grants", "decision:class.members.manage:success", "audit-begin:class.members.manage", "store-enroll"}

	require.True(t, contractHolds(t, []string{"get-class", "grants", "decision:class.members.manage:success", "get-class", "get-user",
		"audit-begin:class.members.manage", "store-enroll"}, lookups, want...), "lookups anywhere, any number of times")
	require.True(t, contractHolds(t, []string{"grants", "decision:class.members.manage:success",
		"audit-begin:class.members.manage", "store-enroll"}, lookups, want...), "no lookups at all")

	require.False(t, contractHolds(t, []string{"grants", "audit-begin:class.members.manage", "decision:class.members.manage:success",
		"store-enroll"}, lookups, want...), "a write before its authorization")
	require.False(t, contractHolds(t, []string{"grants", "decision:class.members.manage:success", "audit-begin:class.members.manage",
		"store-enroll", "store-enroll"}, lookups, want...), "an extra write")
	require.False(t, contractHolds(t, []string{"grants", "decision:class.members.manage:success", "store-enroll"}, lookups, want...),
		"a missing audit step")
	require.False(t, contractHolds(t, []string{"grants", "decision:class.members.manage:success", "list-active",
		"audit-begin:class.members.manage", "store-enroll"}, lookups, want...), "a call the test didn't name as a lookup")
}
