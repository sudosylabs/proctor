// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import "github.com/sudosylabs/proctor/server/httpapi/route"

func validateAcademicRevision(value route.Optional[int64]) error {
	if value.IsNull() {
		return route.InvalidRequest(nil)
	}
	if revision := value.ValuePointer(); revision != nil && *revision <= 0 {
		return route.InvalidRequest(nil)
	}
	return nil
}

// archiveAcademicRequest is the optional revision fence of an academic
// archive. Like every other fence it travels in the JSON body; a DELETE
// without a body archives unconditionally.
type archiveAcademicRequest struct {
	ExpectedRevision route.Optional[int64] `json:"expected_revision"`
}

func academicRevision(request route.Request) (*int64, error) {
	if request.HTTP.ContentLength == 0 && request.HTTP.Header.Get("Content-Type") == "" {
		return nil, nil
	}
	var body archiveAcademicRequest
	if err := request.DecodeJSON(&body); err != nil {
		return nil, err
	}
	if err := validateAcademicRevision(body.ExpectedRevision); err != nil {
		return nil, err
	}
	return body.ExpectedRevision.ValuePointer(), nil
}
