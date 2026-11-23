// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"net/http"
	"strings"

	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/localization"
	"github.com/sudosylabs/proctor/server/model"
)

const (
	integrityFlagSummaryConnectionLoss = "integrity.flag.summary.connection_loss"
	integrityFlagSummaryFocusLoss      = "integrity.flag.summary.focus_loss"
	integrityFlagSummaryBrowser        = "integrity.flag.summary.browser_navigation"
	integrityFlagSummaryBrowserRule    = "integrity.flag.summary.browser_navigation.rule"
	integrityFlagSummaryNative         = "integrity.flag.summary.native"
	integrityFlagSummaryCapability     = "integrity.flag.summary.capability"
	integrityFlagSummaryCount          = "Count"
)

func integrityFlagSummaryLocalizationDefinitions() []localization.Definition {
	plural := func(id string, variables ...string) localization.Definition {
		return localization.Definition{ID: id, Origin: localizationOrigin,
			Variables: append(variables, integrityFlagSummaryCount), PluralVariable: integrityFlagSummaryCount}
	}
	return []localization.Definition{
		plural(integrityFlagSummaryConnectionLoss),
		plural(integrityFlagSummaryFocusLoss),
		plural(integrityFlagSummaryBrowser),
		plural(integrityFlagSummaryBrowserRule, "Rule"),
		plural(integrityFlagSummaryNative, "Check"),
		plural(integrityFlagSummaryCapability, "Check"),
	}
}

// integrityFlagSummary is one localized sentence about a flag: what its
// policy saw and how often, counting evidence beyond what was retained. It
// names a browser rule or a desktop rule (its capability when the flag names
// no rule) by its identifier, never an
// application: Desktop names catalogue applications from its own registry.
func integrityFlagSummary(
	request *http.Request,
	kind model.IntegrityPolicyKind,
	nativeRule string,
	browser *model.BrowserIntegrityGroup,
	evidence int,
	overflow int64,
) string {
	count := int64(evidence) + overflow
	data := map[string]any{integrityFlagSummaryCount: count}
	var id string
	switch {
	case kind == model.IntegrityPolicyConnectionLoss:
		id = integrityFlagSummaryConnectionLoss
	case kind == model.IntegrityPolicyFocusLoss:
		id = integrityFlagSummaryFocusLoss
	case kind == model.IntegrityPolicyBrowserNavigation && browser != nil && browser.RuleID != "":
		id, data["Rule"] = integrityFlagSummaryBrowserRule, browser.RuleID
	case kind == model.IntegrityPolicyBrowserNavigation:
		id = integrityFlagSummaryBrowser
	default:
		check, native := strings.CutPrefix(string(kind), model.NativeIntegrityPolicyNamespace+".")
		id, data["Check"] = integrityFlagSummaryCapability, string(kind)
		if native {
			if nativeRule != "" {
				check = nativeRule
			}
			id, data["Check"] = integrityFlagSummaryNative, check
		}
	}
	return route.TranslateRequest(request, localization.Request{ID: id, TemplateData: data, PluralCount: count}, string(kind))
}
