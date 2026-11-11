// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package route

import (
	"context"
	"net/http"
	"regexp"

	"github.com/sudosylabs/proctor/server/app/apperror"
)

const idempotencyHeader = "Idempotency-Key"

var validIdempotencyKey = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,128}$`)

type idempotencyKeyContextKey struct{}

func idempotencyKeyFromContext(ctx context.Context) string {
	value, _ := ctx.Value(idempotencyKeyContextKey{}).(string)
	return value
}

func (a *API) withIdempotency(next http.Handler, requirement IdempotencyRequirement) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		values, supplied := request.Header[http.CanonicalHeaderKey(idempotencyHeader)]
		if requirement == IdempotencyNone {
			if supplied {
				writeApplicationError(writer, request, a.logger, apperror.New("idempotency.not_supported"))
				return
			}
			next.ServeHTTP(writer, request)
			return
		}
		if !supplied {
			if requirement == IdempotencyRequired {
				writeApplicationError(writer, request, a.logger, apperror.New("idempotency.key_required"))
				return
			}
			next.ServeHTTP(writer, request)
			return
		}
		if len(values) != 1 || !validIdempotencyKey.MatchString(values[0]) {
			writeApplicationError(writer, request, a.logger, apperror.New("idempotency.key_invalid"))
			return
		}
		ctx := context.WithValue(request.Context(), idempotencyKeyContextKey{}, values[0])
		next.ServeHTTP(writer, request.WithContext(ctx))
	})
}

// IdempotencyRequirement says whether a route takes an Idempotency-Key header.
// The kernel validates the header and passes the key in Request.IdempotencyKey;
// replaying an outcome belongs to the use case.
type IdempotencyRequirement string

// IdempotencyNone refuses a key with idempotency.not_supported.
// IdempotencyOptional accepts one. IdempotencyRequired also refuses a request
// without one, with idempotency.key_required. A malformed or repeated key is
// idempotency.key_invalid.
const (
	IdempotencyNone     IdempotencyRequirement = "none"
	IdempotencyOptional IdempotencyRequirement = "optional"
	IdempotencyRequired IdempotencyRequirement = "required"
)

func validIdempotencyRequirement(requirement IdempotencyRequirement) bool {
	return requirement == "" || requirement == IdempotencyNone || requirement == IdempotencyOptional || requirement == IdempotencyRequired
}

// EffectiveIdempotency returns the requirement an endpoint is compiled with: an
// empty one means IdempotencyNone.
func EffectiveIdempotency(requirement IdempotencyRequirement) IdempotencyRequirement {
	if requirement == "" {
		return IdempotencyNone
	}
	return requirement
}
