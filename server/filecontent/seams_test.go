// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package filecontent_test

import (
	"github.com/sudosylabs/proctor/server/app/jobhandler"
	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/filecontent"
)

var (
	_ user.ProfilePictureUploadFiles            = (*filecontent.Content)(nil)
	_ user.ProfilePictureReadFiles              = (*filecontent.Content)(nil)
	_ user.DefaultProfilePictureRenderFiles     = (*filecontent.Content)(nil)
	_ user.DefaultProfilePictureGenerationFiles = (*filecontent.Content)(nil)
	_ jobhandler.FileRevisionContentPurger      = (*filecontent.Content)(nil)
)
