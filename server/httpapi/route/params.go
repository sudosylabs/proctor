// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/channels/web/params.go and context.go.
// Proctor keeps only the matched route's path variables and reads each one by
// the name its path template declares.

package route

import (
	"context"
	"net/http"

	"github.com/gorilla/mux"
	"github.com/sudosylabs/proctor/server/model"
)

type paramsContextKey struct{}

// Params holds the path variables of the matched route, keyed by the names
// its path template declares. The route's parameter kinds have already
// constrained each value's shape. Handlers read typed IDs through PathID,
// provider and opaque IDs through its sibling helpers, and the few values
// that are not identifiers (a batch sequence, a source Session UUID) directly
// by name; none reaches into mux or parses the URL path.
type Params map[string]string

func paramsFromRequest(request *http.Request) Params {
	return Params(mux.Vars(request))
}

func requestParams(ctx context.Context) (Params, bool) {
	params, ok := ctx.Value(paramsContextKey{}).(Params)
	return params, ok
}

func withRequestParams(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		params := paramsFromRequest(request)
		ctx := context.WithValue(request.Context(), paramsContextKey{}, params)
		next.ServeHTTP(writer, request.WithContext(ctx))
	})
}

// PathID reads the named path variable as its typed model identifier. A name
// the route's template does not declare reads as empty, so it is rejected like
// any other invalid identifier: as an invalid request.
func PathID[T interface {
	~string
	IsValid() bool
}](request Request, name string) (T, error) {
	id := T(request.Params[name])
	if !id.IsValid() {
		var zero T
		return zero, InvalidRequest(nil)
	}
	return id, nil
}

// OpaquePathID reads a path variable that has the opaque identifier form but no
// fixed entity type, such as the security preflight identifier, which belongs
// to the native agreement vocabulary rather than the entity catalog.
func OpaquePathID(request Request, name string) (string, error) {
	value := request.Params[name]
	if !model.IsValidOpaqueID(value) {
		return "", InvalidRequest(nil)
	}
	return value, nil
}

// PathProviderID reads a "{name:provider}" path variable. Another party defines
// provider identifiers, so the value stays a string; only its length is checked
// here, the route's parameter kind having fixed its character set.
func PathProviderID(request Request, name string) (string, error) {
	value := request.Params[name]
	if len(value) == 0 || len(value) > model.IdentityProviderMaxLength {
		return "", InvalidRequest(nil)
	}
	return value, nil
}

// OptionalID types an optional identifier member of a request body. The
// application validates it; absence stays nil.
func OptionalID[T ~string](value *string) *T {
	if value == nil {
		return nil
	}
	typed := T(*value)
	return &typed
}
