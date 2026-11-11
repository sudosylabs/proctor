// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"errors"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

// academicNameCursor continues an academic listing ordered by name then
// identity.
type academicNameCursor struct {
	Name string `json:"name"`
	ID   string `json:"id"`
}

func academicNameCursorSpec() route.CursorSpec[academicNameCursor] {
	return route.CursorSpec[academicNameCursor]{
		Label:                "academic",
		MaximumEncodedLength: 1024,
		Members:              []string{"name", "id"},
		Validate: func(cursor academicNameCursor) error {
			if cursor.Name == "" || len(cursor.Name) > 256 || !utf8.ValidString(cursor.Name) || !model.IsValidOpaqueID(cursor.ID) {
				return errors.New("invalid academic keyset")
			}
			return nil
		},
	}
}

// academicNamePageQuery parses the page parameters of a name-ordered
// academic listing.
func academicNamePageQuery(request route.Request) (paging.NameQuery, error) {
	page, err := request.Page()
	if err != nil {
		return paging.NameQuery{}, err
	}
	cursor, present, err := route.DecodePageCursor(page, academicNameCursorSpec())
	if err != nil {
		return paging.NameQuery{}, err
	}
	query := paging.NameQuery{Limit: page.Limit}
	if present {
		query.AfterName, query.AfterID = cursor.Name, cursor.ID
	}
	return query, nil
}

// academicNameListResult presents one name-ordered page.
func academicNameListResult[I, T any](page paging.Page[I], present func(I) T, key func(I) (string, string)) (route.Result, error) {
	response, err := route.NewList(page.Items, page.HasMore, present, func(last I) (string, error) {
		name, id := key(last)
		return route.EncodeCursor(academicNameCursor{Name: name, ID: id}, academicNameCursorSpec())
	})
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, response), nil
}

// academicPeriodCursor continues the Academic Period listing ordered by start,
// name, then identity.
type academicPeriodCursor struct {
	StartAt string `json:"start_at"`
	Name    string `json:"name"`
	ID      string `json:"id"`
}

func academicPeriodCursorSpec() route.CursorSpec[academicPeriodCursor] {
	return route.CursorSpec[academicPeriodCursor]{
		Label:                "academic period",
		MaximumEncodedLength: 1024,
		Members:              []string{"start_at", "name", "id"},
		Validate: func(cursor academicPeriodCursor) error {
			startAt, err := model.ParseRFC3339(cursor.StartAt)
			if err != nil ||
				startAt.IsZero() ||
				cursor.Name == "" ||
				len(cursor.Name) > 256 ||
				!utf8.ValidString(cursor.Name) ||
				!model.AcademicPeriodID(cursor.ID).IsValid() {
				return errors.New("invalid academic period keyset")
			}

			return nil
		},
	}
}

func academicPeriodCursorStart(cursor academicPeriodCursor) time.Time {
	startAt, _ := model.ParseRFC3339(cursor.StartAt)
	return startAt
}
