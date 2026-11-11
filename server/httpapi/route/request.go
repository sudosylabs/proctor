// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/channels/api4/user.go login/logout handlers,
// its session-authenticated boundary, and browser session-cookie behavior.
// Proctor keeps explicit catalog classification and an immutable request
// principal without Mattermost's product-specific handler context.

package route

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

// DecodeJSON strictly decodes a request body into target: one application/json
// Content-Type, no unknown member, and exactly one JSON value. It returns the
// raw cause; Request.DecodeJSON presents it as request.invalid.
func DecodeJSON(request *http.Request, target any) error {
	if err := RequireJSONMediaType(request); err != nil {
		return err
	}
	if request.Body == nil {
		return errors.New("request body is required")
	}
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return err
	}
	return nil
}

// Request is what an operation receives. The kernel fills it after the route's
// credential, assurance, body-limit and idempotency checks have passed.
type Request struct {
	// Context is the request context: cancellation and deadlines only. Pass
	// it to the use case beside Invocation.
	Context context.Context
	// Principal is the authenticated caller, zero on a public route.
	Principal model.Principal
	// Metadata is the request ID, client address and user agent that audit
	// records.
	Metadata model.RequestMetadata
	// Params are the path variables. Read identifiers through PathID and its
	// siblings.
	Params Params
	// HTTP is the underlying request, for headers, cookies, query values and
	// a body an operation streams itself.
	HTTP *http.Request
	// IdempotencyKey is the validated Idempotency-Key header, or "" when the
	// request carried none.
	IdempotencyKey string
}

// Invocation returns the immutable call context every use case takes: the
// Principal, zero on a public route, and the request metadata.
func (r Request) Invocation() invocation.Invocation {
	return invocation.New(r.Principal, r.Metadata)
}

// DecodeJSON strictly decodes the body into target and returns request.invalid
// on any failure.
func (r Request) DecodeJSON(target any) error {
	if err := DecodeJSON(r.HTTP, target); err != nil {
		return InvalidRequest(err)
	}
	return nil
}

// QueryValue returns the named query parameter's first value without
// surrounding whitespace, or "" when the request does not carry it.
func (r Request) QueryValue(name string) string {
	return strings.TrimSpace(r.HTTP.URL.Query().Get(name))
}

// QueryLimit returns the collection page size with the shared default and
// bound.
func (r Request) QueryLimit() (int, error) {
	page, err := r.Page()
	return page.Limit, err
}
