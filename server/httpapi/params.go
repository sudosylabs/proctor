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

package httpapi

import (
	"context"
	"net/http"

	"github.com/gorilla/mux"
	"github.com/sudosylabs/proctor/server/model"
)

type paramsContextKey struct{}

// Params holds the path variables of the matched route, keyed by the names
// its path template declares. The route's parameter kinds have already
// constrained each value's shape; handlers read them through pathID and its
// sibling helpers instead of reaching into mux or parsing URL paths.
type Params map[string]string

func ParamsFromRequest(request *http.Request) Params {
	return Params(mux.Vars(request))
}

func RequestParams(ctx context.Context) (Params, bool) {
	params, ok := ctx.Value(paramsContextKey{}).(Params)
	return params, ok
}

func withRequestParams(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		params := ParamsFromRequest(request)
		ctx := context.WithValue(request.Context(), paramsContextKey{}, params)
		next.ServeHTTP(writer, request.WithContext(ctx))
	})
}

// pathID reads the named path variable as its typed model identifier. A name
// the route's template does not declare reads as empty, so it is rejected
// like any other invalid identifier: as an invalid request.
func pathID[T interface {
	~string
	IsValid() bool
}](request operationRequest, name string) (T, error) {
	id := T(request.params[name])
	if !id.IsValid() {
		var zero T
		return zero, invalidRequestError(nil)
	}
	return id, nil
}

// opaquePathID reads a path variable that has the opaque identifier form but
// no fixed entity type, such as the security preflight identifier, which
// belongs to the native agreement vocabulary rather than the entity catalog.
func opaquePathID(request operationRequest, name string) (string, error) {
	value := request.params[name]
	if !model.IsValidOpaqueID(value) {
		return "", invalidRequestError(nil)
	}
	return value, nil
}

// pathProviderID reads a "{name:provider}" path variable. Another party
// defines provider identifiers, so the value stays a string; only its length
// is checked here, the route's parameter kind having fixed its character set.
func pathProviderID(request operationRequest, name string) (string, error) {
	value := request.params[name]
	if len(value) == 0 || len(value) > model.IdentityProviderMaxLength {
		return "", invalidRequestError(nil)
	}
	return value, nil
}

// optionalIdentifier types an optional identifier member of a request body.
// The application validates it; absence stays nil.
func optionalIdentifier[T ~string](value *string) *T {
	if value == nil {
		return nil
	}
	typed := T(*value)
	return &typed
}
