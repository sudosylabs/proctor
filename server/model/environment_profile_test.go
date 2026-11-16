// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func validEnvironmentProfileSpecification() EnvironmentProfileSpecification {
	return EnvironmentProfileSpecification{
		Name: "python-small", Image: "python-3.12",
		Resources:  EnvironmentResources{CPUMillis: 1000, MemoryBytes: 1 << 30, DiskBytes: 2 << 30},
		Network:    EnvironmentNetworkNone,
		IdleWindow: 10 * time.Minute, MaxTerminals: 4,
	}
}

func TestEnvironmentProfileValidatesEveryField(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	profile, err := NewEnvironmentProfile(NewEnvironmentProfileID(), validEnvironmentProfileSpecification(), NewUserID(), at)
	require.NoError(t, err)
	require.False(t, profile.Retired())

	for name, mutate := range map[string]func(*EnvironmentProfileSpecification){
		"uppercase name":       func(s *EnvironmentProfileSpecification) { s.Name = "Python" },
		"empty image":          func(s *EnvironmentProfileSpecification) { s.Image = "" },
		"image with a slash":   func(s *EnvironmentProfileSpecification) { s.Image = "library/python" },
		"too little CPU":       func(s *EnvironmentProfileSpecification) { s.Resources.CPUMillis = 99 },
		"too much memory":      func(s *EnvironmentProfileSpecification) { s.Resources.MemoryBytes = 65 << 30 },
		"too little disk":      func(s *EnvironmentProfileSpecification) { s.Resources.DiskBytes = 1 << 20 },
		"unknown network":      func(s *EnvironmentProfileSpecification) { s.Network = "open" },
		"short idle window":    func(s *EnvironmentProfileSpecification) { s.IdleWindow = 30 * time.Second },
		"fractional idle":      func(s *EnvironmentProfileSpecification) { s.IdleWindow = 90*time.Second + time.Millisecond },
		"no terminal":          func(s *EnvironmentProfileSpecification) { s.MaxTerminals = 0 },
		"too many terminals":   func(s *EnvironmentProfileSpecification) { s.MaxTerminals = 9 },
		"allowlist is allowed": nil,
	} {
		spec := validEnvironmentProfileSpecification()
		if mutate == nil {
			spec.Network = EnvironmentNetworkAllowlist
			_, err = NewEnvironmentProfile(NewEnvironmentProfileID(), spec, NewUserID(), at)
			require.NoError(t, err, name)
			continue
		}
		mutate(&spec)
		_, err = NewEnvironmentProfile(NewEnvironmentProfileID(), spec, NewUserID(), at)
		require.Error(t, err, name)
	}

	retired := *profile
	retired.RetiredAt = at.Add(-time.Second)
	require.Error(t, retired.Validate(), "a profile cannot retire before it was created")
}

func TestExecutionPolicyIsCoherent(t *testing.T) {
	t.Parallel()
	require.NoError(t, ExecutionPolicy{}.Validate(), "the default is disabled")
	require.NoError(t, ExecutionPolicy{Enabled: true, Profile: NewEnvironmentProfileID(), RecordInput: true, Terminals: 8}.Validate())
	for name, policy := range map[string]ExecutionPolicy{
		"enabled without a profile":  {Enabled: true, Terminals: 1},
		"enabled without a terminal": {Enabled: true, Profile: NewEnvironmentProfileID()},
		"nine terminals":             {Enabled: true, Profile: NewEnvironmentProfileID(), Terminals: 9},
		"disabled with a profile":    {Profile: NewEnvironmentProfileID()},
		"disabled but recording":     {RecordInput: true},
	} {
		require.Error(t, policy.Validate(), name)
	}
}

func TestExamPolicySetFreezesTheExecutionPolicy(t *testing.T) {
	t.Parallel()
	policy := DefaultExamPolicySet()
	policy.Execution = ExecutionPolicy{Enabled: true, Profile: NewEnvironmentProfileID(), RecordInput: true, Terminals: 2}
	encoded, err := EncodeExamPolicySet(policy)
	require.NoError(t, err)
	decoded, err := DecodeExamPolicySet(encoded)
	require.NoError(t, err)
	require.Equal(t, policy.Execution, decoded.Execution)
	require.False(t, decoded.Equal(DefaultExamPolicySet()), "execution is part of the frozen policy and its digest")

	draft := &ExamDraft{ExamID: NewExamID(), Title: "Exam", Policy: DefaultExamPolicySet(), BrowserPolicy: DisabledBrowserPolicy(),
		UpdatedAt: time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC), Revision: 1}
	changed, err := draft.ApplyExecutionPolicy(policy.Execution, draft.UpdatedAt.Add(time.Second))
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, int64(2), draft.Revision)
	changed, err = draft.ApplyExecutionPolicy(policy.Execution, draft.UpdatedAt.Add(time.Minute))
	require.NoError(t, err)
	require.False(t, changed, "the same policy is no change")
}
