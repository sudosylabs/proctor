// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

// institutionAuthorizer authorizes an Action on the installation's singleton
// Institution and returns that Resource for the caller's audit.
type institutionAuthorizer interface {
	AuthorizeInstitution(context.Context, invocation.Invocation, model.Action) (model.Resource, error)
}
