// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package plugin_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/plugin"
)

func TestCallbackSingleValue(t *testing.T) {
	const maxLen = 8

	t.Run("returns the one value", func(t *testing.T) {
		callback := plugin.Callback{Values: map[string][]string{"code": {"abc"}}}
		value, err := callback.SingleValue("code", maxLen)
		require.NoError(t, err)
		require.Equal(t, "abc", value)
	})

	t.Run("accepts a value of exactly the maximum length", func(t *testing.T) {
		callback := plugin.Callback{Values: map[string][]string{"code": {strings.Repeat("a", maxLen)}}}
		value, err := callback.SingleValue("code", maxLen)
		require.NoError(t, err)
		require.Equal(t, strings.Repeat("a", maxLen), value)
	})

	tests := []struct {
		name   string
		values map[string][]string
	}{
		{name: "nil values", values: nil},
		{name: "absent", values: map[string][]string{"state": {"s"}}},
		{name: "no value", values: map[string][]string{"code": {}}},
		{name: "repeated", values: map[string][]string{"code": {"a", "b"}}},
		{name: "empty", values: map[string][]string{"code": {""}}},
		{name: "too long", values: map[string][]string{"code": {strings.Repeat("a", maxLen+1)}}},
		{name: "NUL", values: map[string][]string{"code": {"a\x00b"}}},
		{name: "carriage return", values: map[string][]string{"code": {"a\rb"}}},
		{name: "line feed", values: map[string][]string{"code": {"a\nb"}}},
	}
	for _, test := range tests {
		t.Run("rejects "+test.name, func(t *testing.T) {
			value, err := plugin.Callback{Values: test.values}.SingleValue("code", maxLen)
			require.ErrorIs(t, err, plugin.ErrInvalidCallback)
			require.Empty(t, value)
		})
	}
}

func TestCallbackOptionalSingleValue(t *testing.T) {
	const maxLen = 8

	accepted := []struct {
		name   string
		values map[string][]string
		want   string
	}{
		{name: "absent", values: map[string][]string{}, want: ""},
		{name: "present", values: map[string][]string{"ticket": {"ST-1"}}, want: "ST-1"},
		{name: "present but empty", values: map[string][]string{"ticket": {""}}, want: ""},
	}
	for _, test := range accepted {
		t.Run("accepts "+test.name, func(t *testing.T) {
			value, err := plugin.Callback{Values: test.values}.OptionalSingleValue("ticket", maxLen)
			require.NoError(t, err)
			require.Equal(t, test.want, value)
		})
	}

	rejected := []struct {
		name   string
		values map[string][]string
	}{
		{name: "repeated", values: map[string][]string{"ticket": {"a", "b"}}},
		{name: "no value", values: map[string][]string{"ticket": {}}},
		{name: "too long", values: map[string][]string{"ticket": {strings.Repeat("a", maxLen+1)}}},
		{name: "line feed", values: map[string][]string{"ticket": {"a\nb"}}},
	}
	for _, test := range rejected {
		t.Run("rejects "+test.name, func(t *testing.T) {
			value, err := plugin.Callback{Values: test.values}.OptionalSingleValue("ticket", maxLen)
			require.ErrorIs(t, err, plugin.ErrInvalidCallback)
			require.Empty(t, value)
		})
	}
}
