// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBrowserPolicyRuleDocumentLabel(t *testing.T) {
	const rule = `{"rule_id":"docs","origin":"https://docs.example","path_prefix":"/",` +
		`"host_match":"exact","allow_redirects":false,"blocked_navigation_outcome":"record",` +
		`"institution_http_exception":false`

	var labeled browserPolicyDocument
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":true,"start_rule_id":"docs","rules":[`+rule+`,"label":" Course notes "}]}`), &labeled))
	policy, err := labeled.model()
	require.NoError(t, err)
	assert.Equal(t, "Course notes", policy.Rules[0].Label, "the model normalizes the label")
	assert.Equal(t, "Course notes", browserPolicyDocumentFromModel(policy).Rules[0].Label, "the label is read back")

	var unlabeled browserPolicyRuleDocument
	require.NoError(t, json.Unmarshal([]byte(rule+`}`), &unlabeled), "the label is optional")
	encoded, err := json.Marshal(unlabeled)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "label", "an absent label is left out")

	var rejected browserPolicyRuleDocument
	assert.Error(t, json.Unmarshal([]byte(rule+`,"label":null}`), &rejected), "a null label is rejected")
	assert.Error(t, json.Unmarshal([]byte(rule+`,"label":"a","extra":1}`), &rejected), "unknown members stay rejected")

	var tooLong browserPolicyDocument
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":true,"start_rule_id":"docs","rules":[`+rule+`,"label":"`+strings.Repeat("x", 101)+`"}]}`), &tooLong))
	_, err = tooLong.model()
	assert.Error(t, err, "the use case refuses an overlong label")
}
