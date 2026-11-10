// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"time"

	"github.com/sudosylabs/proctor/server/app/jobhandler"
	"github.com/sudosylabs/proctor/server/app/user"
)

func constructProfilesAndFiles(
	deps Dependencies,
	foundation applicationFoundation,
	access accessAcademicConstruction,
) (profileFileConstruction, error) {
	// The Job runtime that wakes on a proposal exists only after the profile
	// service, so constructJobs attaches the wake to these proposals later.
	var defaultJobs *jobhandler.DefaultProfilePictureJobs
	var proposals user.DefaultPictureJobs
	if deps.Store.Job() != nil {
		defaultJobs = jobhandler.NewDefaultProfilePictureJobs(jobhandler.DefaultProfilePictureJobsDependencies{Jobs: deps.Store.Job()})
		proposals = defaultJobs
	}
	users, err := user.New(user.Dependencies{
		Store: deps.Store, Authorizer: access.authorization, Auditor: foundation.audit.MutationAudit(),
		SettingsAuditor: foundation.audit.UserSettingsAudit(), Effects: userRealtimeEffects{realtime: foundation.realtime},
		DefaultJobs: proposals,
		Uploads:     deps.FileContent, Reads: deps.FileContent, Fallback: deps.FileContent, Defaults: deps.FileContent,
		Now: time.Now,
	})
	if err != nil {
		return profileFileConstruction{}, err
	}
	return profileFileConstruction{users: users, defaultJobs: defaultJobs}, nil
}
