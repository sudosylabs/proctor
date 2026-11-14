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
	"time"

	"github.com/stretchr/testify/require"
)

func TestCanonicalizeExamPluginSectionIgnoresSpellingAndKeyOrder(t *testing.T) {
	t.Parallel()
	first, err := CanonicalizeExamPluginSection(json.RawMessage(` { "network": "none", "image": {"name":"go","tag":"1.25"}, "record_input": true } `))
	require.NoError(t, err)
	second, err := CanonicalizeExamPluginSection(json.RawMessage(`{"record_input":true,"image":{"tag":"1.25","name":"go"},"network":"none"}`))
	require.NoError(t, err)
	require.Equal(t, `{"image":{"name":"go","tag":"1.25"},"network":"none","record_input":true}`, string(first))
	require.Equal(t, string(first), string(second))
}

func TestCanonicalizeExamPluginSectionRejectsWhatIsNotABoundedObject(t *testing.T) {
	t.Parallel()
	for name, section := range map[string]string{
		"empty":            ``,
		"array":            `[]`,
		"string":           `"text"`,
		"null":             `null`,
		"duplicate member": `{"a":1,"a":2}`,
		"fraction":         `{"a":1.5}`,
		"exponent":         `{"a":1e3}`,
		"trailing value":   `{"a":1} {}`,
		"oversized":        `{"a":"` + strings.Repeat("x", ExamPluginSectionMaxBytes) + `"}`,
	} {
		_, err := CanonicalizeExamPluginSection(json.RawMessage(section))
		require.Error(t, err, name)
	}
}

func TestExamPluginSectionsDigestFollowsContentNotOrder(t *testing.T) {
	t.Parallel()
	empty, err := ExamPluginSectionsDigest(nil)
	require.NoError(t, err)
	encoded, err := EncodeExamPluginSections(nil)
	require.NoError(t, err)
	require.Equal(t, `{}`, string(encoded))

	sections := ExamPluginSections{
		"execution": json.RawMessage(`{"network":"none"}`),
		"display":   json.RawMessage(`{"screens":1}`),
	}
	withSections, err := ExamPluginSectionsDigest(sections)
	require.NoError(t, err)
	require.NotEqual(t, empty, withSections)
	encoded, err = EncodeExamPluginSections(sections)
	require.NoError(t, err)
	require.Equal(t, `{"display":{"screens":1},"execution":{"network":"none"}}`, string(encoded))

	decoded, err := DecodeExamPluginSections([]byte(`{ "execution": {"network":"none"}, "display": {"screens":1} }`))
	require.NoError(t, err)
	again, err := ExamPluginSectionsDigest(decoded)
	require.NoError(t, err)
	require.Equal(t, withSections, again)

	_, err = DecodeExamPluginSections([]byte(`{"Not An ID":{}}`))
	require.Error(t, err)
	_, err = DecodeExamPluginSections([]byte(`{"execution":[]}`))
	require.Error(t, err)
	require.Error(t, ExamPluginSections{"execution": json.RawMessage(`{ "network": "none" }`)}.Validate(), "a stored section must already be canonical")
}

func TestExamDraftApplyPluginSectionSetsReplacesAndRemoves(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	draft, err := NewExamDraft(NewExamID(), "Algorithms", "", DefaultExamPolicySet(), at)
	require.NoError(t, err)
	require.Empty(t, draft.PluginSections)

	changed, err := draft.ApplyPluginSection("execution", json.RawMessage(`{ "network": "none" }`), at.Add(time.Second))
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, int64(2), draft.Revision)
	require.Equal(t, `{"network":"none"}`, string(draft.PluginSections["execution"]))

	changed, err = draft.ApplyPluginSection("execution", json.RawMessage(`{"network":"none"}`), at.Add(2*time.Second))
	require.NoError(t, err)
	require.False(t, changed, "the same canonical section is a no-op")
	require.Equal(t, int64(2), draft.Revision)

	changed, err = draft.ApplyPluginSection("execution", nil, at.Add(3*time.Second))
	require.NoError(t, err)
	require.True(t, changed)
	require.Empty(t, draft.PluginSections)

	changed, err = draft.ApplyPluginSection("execution", nil, at.Add(4*time.Second))
	require.NoError(t, err)
	require.False(t, changed, "removing an absent section is a no-op")

	_, err = draft.ApplyPluginSection("Bad ID", json.RawMessage(`{}`), at)
	require.Error(t, err)
	_, err = draft.ApplyPluginSection("execution", json.RawMessage(`[]`), at)
	require.Error(t, err)
}

func TestExamRevisionFreezesPluginSectionsIntoItsDigest(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	policy, err := NewExamRevisionPolicy(DefaultExamPolicySet())
	require.NoError(t, err)
	specification := ExamRevisionSpecification{
		ID: NewExamRevisionID(), ExamID: NewExamID(), Number: 1, SourceDraftRevision: 3,
		Title: "Algorithms", Policy: policy, Capacity: DefaultExamCapacityPolicy(),
		PublishedByUserID: NewUserID(), PublishedAt: at, Kind: ExamRevisionPublicationStandard,
	}
	without, err := NewExamRevision(specification)
	require.NoError(t, err)

	section := json.RawMessage(`{"network":"none"}`)
	specification.PluginSections = ExamPluginSections{"execution": section}
	with, err := NewExamRevision(specification)
	require.NoError(t, err)
	require.NotEqual(t, without.ContentDigest, with.ContentDigest)
	require.NotEqual(t, without.PluginSectionsDigest, with.PluginSectionsDigest)

	section[0] = '!'
	frozen, ok := with.PluginSections.Section("execution")
	require.True(t, ok)
	require.Equal(t, `{"network":"none"}`, string(frozen), "construction clones caller-owned sections")

	tampered := with.Clone()
	tampered.PluginSections["execution"] = json.RawMessage(`{"network":"open"}`)
	require.Error(t, tampered.Validate())

	correction, err := NewLiveCorrectionExamRevision(with, LiveCorrectionExamRevisionSpecification{
		ID: NewExamRevisionID(), Number: 2, InstructionsMarkdown: "Corrected",
		CandidateSummary: "Instructions changed.", AffectedCapabilities: []CandidateCapability{CandidateCapabilitySubmission, CandidateCapabilityWorkspace},
		PublishedByUserID: NewUserID(), PublishedAt: at.Add(time.Minute),
	})
	require.NoError(t, err)
	require.Equal(t, with.PluginSectionsDigest, correction.PluginSectionsDigest, "a live correction keeps the base Revision's sections")
}
