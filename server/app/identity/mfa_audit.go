// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

func (s *Service) mfaAuditResource(ctx context.Context) (model.Resource, error) {
	institution, err := s.institutions.GetSingleton(ctx)
	if err != nil {
		return model.Resource{}, mfaStoreErrors.Translate(err)
	}
	return model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()}, nil
}

func (s *Service) failMFAMutation(
	ctx context.Context,
	auditID model.AuditEventID,
	err error,
) error {
	mapped := mfaStoreErrors.Translate(err)
	code := "authentication.mfa.unavailable"
	if failure, ok := apperror.As(mapped); ok {
		code = failure.Code()
	}
	if auditErr := s.completeMFAAudit(
		ctx, auditID, model.AuditStatusFail, code, nil,
	); auditErr != nil {
		return errors.Join(mapped, auditErr)
	}
	return mapped
}

// beginMFAAudit records the attempt of an MFA mutation. Self-service MFA
// mutations are audited against the Institution, with the acting User as
// their subject. An administrator reset names the target User as its
// resource, which is its own subject.
func (s *Service) beginMFAAudit(
	ctx context.Context,
	invocation invocation.Invocation,
	action model.Action,
	resource model.Resource,
	prior any,
) (model.AuditEventID, error) {
	if s.securityAudit == nil {
		return "", access.AuditUnavailable(errors.New("MFA audit service is unavailable"))
	}

	var subject model.UserID
	if resource.Type != model.ResourceUser {
		subject = invocation.Principal().UserID
	}

	event, err := s.securityAudit.BeginCriticalActionForSubject(
		ctx,
		invocation.Principal(),
		subject,
		action,
		resource,
		invocation.RequestMetadata(),
		nil,
		prior,
	)
	if err != nil {
		return "", err
	}

	return event.ID, nil
}

// completeMFAAudit records the terminal outcome of an MFA mutation the
// Service itself, rather than its atomic Store operation, ends.
func (s *Service) completeMFAAudit(
	ctx context.Context,
	auditID model.AuditEventID,
	status model.AuditStatus,
	errorCode string,
	result any,
) error {
	if s.securityAudit == nil {
		return access.AuditUnavailable(errors.New("MFA audit service is unavailable"))
	}
	_, err := s.securityAudit.CompleteCriticalAction(ctx, auditID, status, errorCode, result)
	return err
}
