// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/channels/app/authentication.go,
// server/channels/app/login.go, and server/channels/app/session.go. Proctor
// keeps the single application authentication flow, generic login failures,
// server-side revocable sessions, activity debouncing, and boundary-safe
// errors while using split hashed access/refresh credentials and rotation.

package identity

import (
	"context"

	"github.com/sudosylabs/proctor/server/logging"
)

func (s *Service) warn(ctx context.Context, message string, err error) {
	if s.diagnostics == nil {
		return
	}
	s.diagnostics.WarnContext(ctx, message, logging.Err(err))
}

func recordOperational(recorder OperationalRecorder, subsystem, event string, err error) {
	if recorder != nil {
		recorder.RecordOperational(subsystem, event, err)
	}
}
