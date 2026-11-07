// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package mail

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/localization"
)

func TestLocalizationDefinitionsMatchCatalog(t *testing.T) {
	t.Parallel()
	localizer, err := localization.New(os.DirFS("../../i18n"), localization.EnglishLocale)
	require.NoError(t, err)
	// Restrict this assertion to mail-owned definitions. The full cross-consumer
	// exact-set check lives in ptool.
	definitions := LocalizationDefinitions()
	require.Len(t, definitions, 384, "mail localization definitions")
	for _, definition := range definitions {
		request := localization.Request{ID: definition.ID, TemplateData: representativeCopyData}
		if definition.PluralVariable != "" {
			request = localization.Request{ID: definition.ID, PluralCount: 2}
		}
		_, err := localizer.ResolveRequest(localization.EnglishLocale, request)
		require.NoError(t, err, "resolve %q", definition.ID)
	}
}
