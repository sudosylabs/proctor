// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package desktoprelease

import (
	"crypto/ed25519"

	"github.com/sudosylabs/proctor/server/model"
)

// VerifiedCatalog is the Desktop builds this server admits. It is empty
// until coordinated activation supplies signed Desktop target artifacts and
// capability matrices. Server releases must embed exact verified tuples
// here, so configuration and Institution policy cannot invent compatible
// released builds. Development skips login compatibility checks instead of
// changing this catalog.
func VerifiedCatalog() ([]model.DesktopBuildTuple, error) {
	// Real activation must add verified target artifacts and the admitted release
	// public keys together. Synthetic certification belongs only in tests.
	return verifyReleases([]admittedRelease{}, map[string]ed25519.PublicKey{})
}

type admittedRelease struct {
	expectation Expectation
	artifacts   Artifacts
}

func verifyReleases(releases []admittedRelease, keys map[string]ed25519.PublicKey) ([]model.DesktopBuildTuple, error) {
	result := make([]model.DesktopBuildTuple, 0, len(releases))
	for _, release := range releases {
		build, err := Verify(release.expectation, release.artifacts, keys)
		if err != nil {
			return nil, err
		}
		result = append(result, build)
	}
	return result, nil
}
