// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package localization

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Definition declares one message. Origin is a stable package label for
// tooling; Variables are the exact template names every locale must use.
type Definition struct {
	ID             string
	Origin         string
	Variables      []string
	PluralVariable string
}

// MergeDefinitions returns a sorted copy. A duplicate ID is rejected even when
// both declarations match, so every message has one owner.
func MergeDefinitions(groups ...[]Definition) ([]Definition, error) {
	count := 0
	for _, group := range groups {
		count += len(group)
	}
	definitions := make([]Definition, 0, count)
	owners := make(map[string]string, count)
	for _, group := range groups {
		for _, candidate := range group {
			definition, err := normalizeDefinition(candidate)
			if err != nil {
				return nil, err
			}
			if owner, exists := owners[definition.ID]; exists {
				return nil, fmt.Errorf("localization id %q is declared by both %q and %q", definition.ID, owner, definition.Origin)
			}
			owners[definition.ID] = definition.Origin
			definitions = append(definitions, definition)
		}
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].ID < definitions[j].ID })
	return definitions, nil
}

func normalizeDefinition(definition Definition) (Definition, error) {
	if !keyPattern.MatchString(definition.ID) {
		return Definition{}, fmt.Errorf("invalid localization id %q", definition.ID)
	}
	definition.Origin = strings.TrimSpace(definition.Origin)
	if definition.Origin == "" {
		return Definition{}, fmt.Errorf("localization id %q has no origin", definition.ID)
	}
	definition.Variables = append([]string(nil), definition.Variables...)
	if definition.PluralVariable != "" && !validVariable(definition.PluralVariable) {
		return Definition{}, fmt.Errorf("localization id %q has invalid plural variable %q", definition.ID, definition.PluralVariable)
	}
	sort.Strings(definition.Variables)
	previous := ""
	for _, variable := range definition.Variables {
		if !validVariable(variable) {
			return Definition{}, fmt.Errorf("localization id %q has invalid variable %q", definition.ID, variable)
		}
		if variable == previous {
			return Definition{}, fmt.Errorf("localization id %q repeats variable %q", definition.ID, variable)
		}
		previous = variable
	}
	return definition, nil
}

func validVariable(variable string) bool {
	if variable == "" {
		return false
	}
	for index, character := range variable {
		if index == 0 && !unicodeLetter(character) {
			return false
		}
		if index > 0 && !unicodeLetter(character) && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

func unicodeLetter(character rune) bool {
	return character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z'
}

// ValidateDefinitions checks that English has exactly the declared IDs, each
// with the declared template variables.
func (l *Localizer) ValidateDefinitions(definitions []Definition) error {
	return l.validateDefinitions(definitions, nil)
}

// ValidateDefinitionsWithDelegatedPrefixes skips IDs under prefixes, which
// another checker owns. A prefix must end at a namespace boundary and must not
// cover any of definitions.
func (l *Localizer) ValidateDefinitionsWithDelegatedPrefixes(definitions []Definition, prefixes ...string) error {
	delegated, err := normalizeDelegatedPrefixes(prefixes)
	if err != nil {
		return err
	}
	return l.validateDefinitions(definitions, delegated)
}

func (l *Localizer) validateDefinitions(definitions []Definition, delegated []string) error {
	if l == nil {
		return errors.New("localizer is nil")
	}

	merged, err := MergeDefinitions(definitions)
	if err != nil {
		return err
	}

	english := l.catalogs[EnglishLocale]
	declared := make(map[string]Definition, len(merged))
	for _, definition := range merged {
		if prefix := matchingPrefix(definition.ID, delegated); prefix != "" {
			return fmt.Errorf("localization id %q from %q belongs to delegated prefix %q", definition.ID, definition.Origin, prefix)
		}

		declared[definition.ID] = definition
		message, exists := english[definition.ID]
		if !exists {
			return fmt.Errorf("declared localization id %q from %q is missing from en.json", definition.ID, definition.Origin)
		}

		if message.placeholders != strings.Join(definition.Variables, ",") {
			return fmt.Errorf(
				"localization id %q declares variables %q but en.json uses %q",
				definition.ID,
				strings.Join(definition.Variables, ","),
				message.placeholders,
			)
		}

		if message.plural != (definition.PluralVariable != "") {
			return fmt.Errorf("localization id %q plural declaration does not match en.json", definition.ID)
		}
	}

	for id := range english {
		if _, exists := declared[id]; !exists && matchingPrefix(id, delegated) == "" {
			return fmt.Errorf("en.json contains orphan localization id %q", id)
		}
	}

	return nil
}

func normalizeDelegatedPrefixes(prefixes []string) ([]string, error) {
	delegated := append([]string(nil), prefixes...)
	sort.Strings(delegated)
	previous := ""
	for _, prefix := range delegated {
		if prefix == "" || !strings.HasSuffix(prefix, ".") || !keyPattern.MatchString(strings.TrimSuffix(prefix, ".")) {
			return nil, fmt.Errorf("invalid delegated localization prefix %q", prefix)
		}
		if prefix == previous {
			return nil, fmt.Errorf("duplicate delegated localization prefix %q", prefix)
		}
		previous = prefix
	}
	return delegated, nil
}

func matchingPrefix(id string, prefixes []string) string {
	for _, prefix := range prefixes {
		if strings.HasPrefix(id, prefix) {
			return prefix
		}
	}
	return ""
}

// MissingDefinitions lists definitions absent from locale, for measuring
// coverage. A partial catalog is valid at runtime because resolution falls back.
func (l *Localizer) MissingDefinitions(locale string, definitions []Definition) ([]Definition, error) {
	if l == nil {
		return nil, errors.New("localizer is nil")
	}
	rawLocale := locale
	locale, ok := normalizeLocale(locale)
	if !ok {
		return nil, fmt.Errorf("invalid locale %q", rawLocale)
	}
	catalog, exists := l.catalogs[locale]
	if !exists {
		return nil, fmt.Errorf("locale %q has no catalog", locale)
	}
	merged, err := MergeDefinitions(definitions)
	if err != nil {
		return nil, err
	}
	missing := make([]Definition, 0)
	for _, definition := range merged {
		if _, exists := catalog[definition.ID]; !exists {
			missing = append(missing, definition)
		}
	}
	return missing, nil
}
