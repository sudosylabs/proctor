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

// VerifiedCatalog returns the Desktop builds this server admits. It is compiled
// in so neither configuration nor institution policy can add a build, and it
// is empty until signed releases exist; development skips the login check
// instead of changing it.
func VerifiedCatalog() ([]model.DesktopBuildTuple, error) {
	// Add release artifacts and their public keys together. Synthetic
	// certification belongs only in tests.
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
