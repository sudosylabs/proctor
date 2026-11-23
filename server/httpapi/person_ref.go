// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"github.com/sudosylabs/proctor/server/app/people"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

// personRefResponse names a person beside an ID the record already carries.
// DisplayName is absent only for a removed account.
type personRefResponse struct {
	ID                string `json:"id"`
	DisplayName       string `json:"display_name,omitempty"`
	Username          string `json:"username,omitempty"`
	ProfilePictureURL string `json:"profile_picture_url,omitempty"`
}

// personRef returns nil for a zero ID, so an optional actor stays absent.
func personRef(refs people.Refs, id model.UserID) *personRefResponse {
	ref, ok := refs.Get(id)
	if !ok {
		return nil
	}
	response := &personRefResponse{ID: ref.ID.String()}
	response.DisplayName = ref.DisplayName
	if ref.Visible && !ref.Removed() {
		response.Username = ref.Username
		response.ProfilePictureURL = "/api/v1/users/" + ref.ID.String() + "/profile-picture"
	}
	return response
}

// namePeople names the people an authorized record refers to. The resolver
// reports a failed read with its area's unavailable code.
func namePeople(request route.Request, resolver *people.Resolver, ids ...model.UserID) (people.Refs, error) {
	return resolver.Resolve(request.Context, request.Invocation().Principal(), ids)
}
