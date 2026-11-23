// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import "github.com/sudosylabs/proctor/server/model"

type roleRefResponse struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
}

type scopeRefResponse struct {
	Type        model.RoleScopeType `json:"type"`
	ID          string              `json:"id"`
	DisplayName string              `json:"display_name,omitempty"`
}

type examRefResponse struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type classRefResponse struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}
