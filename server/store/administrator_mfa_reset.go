// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import "github.com/sudosylabs/proctor/server/model"

// AdministratorMFAReset is accepted only from the offline host command. The
// store confirms the installation and active administrator under the
// serving-lease and protected-administrator fences, and refuses unless
// reenrollment and a primary sign-in path remain; it never adds or changes a
// primary credential.
type AdministratorMFAReset struct {
	InstitutionID model.InstitutionID
	UserID        model.UserID
	MFAEnabled    bool
	Capabilities  AccessDeploymentCapabilities
}

type AdministratorMFAResetResult struct {
	RecordID model.AdministratorRecoveryRecordID
	Recovery *model.UserMFARecovery
}
