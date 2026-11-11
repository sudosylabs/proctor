// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package architecture_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	dependencyReferencePath  = ".agents/skills/server-boundaries/references/dependencies.md"
	dependencyReferenceStart = "<!-- Generated from dependencyRules by TestDependencyReferenceMatchesRules. -->\n"
	dependencyReferenceEnd   = "<!-- End of generated dependency rules. -->\n"
)

// TestDependencyReferenceMatchesRules keeps the rule table in the dependency
// reference equal to dependencyRules, so the prose cannot drift from the
// policy. On a mismatch it prints the block to paste between the markers.
func TestDependencyReferenceMatchesRules(t *testing.T) {
	path := filepath.Join("..", "..", filepath.FromSlash(dependencyReferencePath))
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		t.Skip("the dependency reference is unavailable outside the monorepo checkout")
	}
	require.NoError(t, err)

	document := string(content)
	start := strings.Index(document, dependencyReferenceStart)
	end := strings.Index(document, dependencyReferenceEnd)
	require.True(t, start >= 0 && end > start, "%s lacks the generated dependency rule markers", dependencyReferencePath)
	committed := document[start+len(dependencyReferenceStart) : end]
	generated := renderDependencyRules(dependencyRules)

	if committed != generated {
		assert.Fail(t, dependencyReferencePath+" is stale", "replace the lines between its generated-rule markers with:\n%s", generated)
	}
}

func renderDependencyRules(rules []dependencyRule) string {
	var out strings.Builder
	fmt.Fprintf(&out, "\n*Infrastructure* is %s.\n\n", renderPatterns(standardInfrastructure))
	out.WriteString("| Rule | Packages | Server and repository imports | Third-party imports | Standard library denied |\n")
	out.WriteString("| --- | --- | --- | --- | --- |\n")
	for _, rule := range rules {
		project, thirdParty, standard := renderAccess(rule.project), renderAccess(rule.thirdParty), renderStandard(rule.deniedStandard)
		if rule.denyEveryImport {
			project, thirdParty, standard = "none", "none", "all"
		}
		fmt.Fprintf(&out, "| %s | %s | %s | %s | %s |\n", rule.name, renderPatterns(rule.sources), project, thirdParty, standard)
	}
	out.WriteString("\n")
	return out.String()
}

func renderAccess(access importAccess) string {
	switch {
	case !access.allowUnlisted && len(access.allowed) == 0:
		return "none"
	case !access.allowUnlisted:
		return renderPatterns(access.allowed)
	case len(access.denied) == 0:
		return "any"
	case len(access.allowed) == 0:
		return "any except " + renderPatterns(access.denied)
	default:
		return "any except " + renderPatterns(access.denied) + ", but " + renderPatterns(access.allowed)
	}
}

func renderStandard(denied []pathPattern) string {
	named := []struct {
		name     string
		patterns []pathPattern
	}{
		{name: "infrastructure", patterns: standardInfrastructure},
		{name: "infrastructure except `net/http`", patterns: standardInfrastructureExceptHTTP},
		{name: "infrastructure except `io/fs`", patterns: standardInfrastructureExceptFS},
		{name: "infrastructure except `os`", patterns: standardInfrastructureExceptOS},
	}
	if len(denied) == 0 {
		return "none"
	}
	for _, set := range named {
		if slices.Equal(set.patterns, denied) {
			return set.name
		}
	}
	return renderPatterns(denied)
}

func renderPatterns(patterns []pathPattern) string {
	rendered := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		path := "`" + displayPackage(pattern.path) + "`"
		switch pattern.match {
		case matchSubtree:
			path += " and below"
		case matchDescendants:
			path = "below " + path
		}
		rendered = append(rendered, path)
	}
	return strings.Join(rendered, ", ")
}

// displayPackage names a project package by its path within the repository,
// the server module root as `server`.
func displayPackage(path string) string {
	if path == serverModule {
		return "server"
	}
	if relative, ok := strings.CutPrefix(path, serverModule+"/"); ok {
		return relative
	}
	if relative, ok := strings.CutPrefix(path, repositoryModule+"/"); ok {
		return relative
	}
	return path
}
