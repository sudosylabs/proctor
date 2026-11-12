// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

type mailRekeyRequest struct {
	RetiringKeyID string `json:"retiring_key_id"`
}

type mailRekeyResponse struct {
	JobID         string `json:"job_id"`
	PrimaryKeyID  string `json:"primary_key_id"`
	RetiringKeyID string `json:"retiring_key_id"`
	CreatedAt     string `json:"created_at"`
}

type mailRekeyProgressResponse struct {
	Current int64  `json:"current"`
	Total   int64  `json:"total"`
	Stage   string `json:"stage"`
}

type mailRekeyProofResponse struct {
	NonPrimaryReferences int64 `json:"non_primary_references"`
	RetiringReferences   int64 `json:"retiring_references"`
	RetirementSafe       bool  `json:"retirement_safe"`
}

type mailRekeyStatusResponse struct {
	JobID           string                     `json:"job_id"`
	Status          model.JobStatus            `json:"status"`
	PrimaryKeyID    string                     `json:"primary_key_id"`
	RetiringKeyID   string                     `json:"retiring_key_id"`
	CreatedAt       string                     `json:"created_at"`
	UpdatedAt       string                     `json:"updated_at"`
	CompletedAt     string                     `json:"completed_at,omitempty"`
	PublicErrorCode string                     `json:"public_error_code,omitempty"`
	AttemptCount    int                        `json:"attempt_count"`
	MaximumAttempts int                        `json:"maximum_attempts"`
	Processed       int64                      `json:"processed"`
	Reencrypted     int64                      `json:"reencrypted"`
	Progress        *mailRekeyProgressResponse `json:"progress,omitempty"`
	Proof           *mailRekeyProofResponse    `json:"proof,omitempty"`
}

type mailPayloadKeyUsageResponse struct {
	KeyID            string `json:"key_id"`
	ActiveReferences int64  `json:"active_references"`
}

type mailKeyStateResponse struct {
	PrimaryKeyID         string                        `json:"primary_key_id"`
	RequiredPrimaryKeyID string                        `json:"required_primary_key_id,omitempty"`
	Active               []mailPayloadKeyUsageResponse `json:"active"`
}

type mailDeliveryResponse struct {
	ID                 string                  `json:"id"`
	OccurrenceID       string                  `json:"occurrence_id"`
	TargetUserID       string                  `json:"target_user_id,omitempty"`
	TargetInvitationID string                  `json:"target_invitation_id,omitempty"`
	TemplateKey        model.MailTemplateKey   `json:"template_key"`
	TemplateDigest     string                  `json:"template_digest"`
	MaskedRecipient    string                  `json:"masked_recipient"`
	State              model.MailDeliveryState `json:"state"`
	CreatedAt          string                  `json:"created_at"`
	UpdatedAt          string                  `json:"updated_at"`
	MessageDate        string                  `json:"message_date"`
	Deadline           string                  `json:"deadline"`
	MessageID          string                  `json:"message_id"`
	AttemptCount       int                     `json:"attempt_count"`
	AcceptedAt         string                  `json:"accepted_at,omitempty"`
	FailedAt           string                  `json:"failed_at,omitempty"`
	PublicFailureCode  string                  `json:"public_failure_code,omitempty"`
}

// mailDeliveryListResponse is the collection envelope; the first page also
// carries the total (see route.CountedList).
type mailDeliveryListResponse struct {
	Items             []mailDeliveryResponse `json:"items"`
	NextCursor        string                 `json:"next_cursor,omitempty"`
	Total             *int                   `json:"total,omitempty"`
	TotalIsLowerBound *bool                  `json:"total_is_lower_bound,omitempty"`
}

type mailDeliveryMetricResponse struct {
	TemplateKey                    model.MailTemplateKey   `json:"template_key"`
	State                          model.MailDeliveryState `json:"state"`
	OutcomeCode                    string                  `json:"outcome_code"`
	Count                          uint64                  `json:"count"`
	AttemptCount                   uint64                  `json:"attempt_count"`
	ProcessingLatencyMillis        int64                   `json:"processing_latency_millis"`
	MaximumProcessingLatencyMillis int64                   `json:"maximum_processing_latency_millis"`
}

type mailQueueMetricResponse struct {
	TemplateKey     model.MailTemplateKey   `json:"template_key"`
	State           model.MailDeliveryState `json:"state"`
	OutcomeCode     string                  `json:"outcome_code"`
	Count           int64                   `json:"count"`
	OldestAgeMillis int64                   `json:"oldest_age_millis"`
	HealthCode      string                  `json:"health_code"`
	Truncated       bool                    `json:"truncated"`
}

type mailMetricsResponse struct {
	Deliveries []mailDeliveryMetricResponse `json:"deliveries"`
	Queues     []mailQueueMetricResponse    `json:"queues"`
	HealthCode string                       `json:"health_code"`
}

type mailDeliveryCursor struct {
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
}

type mailHandlers struct{ app MailApplication }

func mailResource(application MailApplication) route.Resource {
	h := mailHandlers{app: application}
	return route.NewResource("mail",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/mail/keys",
			Auth:   route.AuthStrongRecentSessionRequired,
			Errors: []string{"authorization.denied", "authorization.request.invalid", "authorization.unavailable", "mail.unavailable", "resource.not_found"},
			Handle: h.getKeyState,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/mail/rekey",
			Auth:   route.AuthStrongRecentSessionRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"mail.rekey.invalid",
				"mail.rekey.conflict",
				"mail.unavailable",
				"resource.not_found",
			},
			Handle: h.startRekey,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/mail/rekey/{job_id}",
			Auth:   route.AuthStrongRecentSessionRequired,
			Errors: operatorReadErrorCodes("request.invalid", "resource.not_found", "mail.unavailable"),
			Handle: h.getRekeyStatus,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/mail/test",
			Auth:   route.AuthRecentSessionRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"mail.recipient_unverified",
				"mail.recipient_ineligible",
				"mail.test.rate_limited",
				"mail.conflict",
				"mail.unavailable",
				"resource.not_found",
			},
			Handle: h.sendTest,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/mail/metrics",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{"authorization.denied", "authorization.request.invalid", "authorization.unavailable", "mail.unavailable", "resource.not_found"},
			Handle: h.getMetrics,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/mail/deliveries",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"mail.query.invalid",
				"mail.unavailable",
				"request.invalid",
				"resource.not_found",
			},
			Handle: h.listDeliveries,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/mail/deliveries/{mail_delivery_id}",
			Auth:   route.AuthPrincipalRequired,
			Errors: operatorReadErrorCodes("request.invalid", "resource.not_found", "mail.unavailable"),
			Handle: h.getDelivery,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/mail/deliveries/{mail_delivery_id}/cancel",
			Auth:   route.AuthRecentSessionRequired,
			Errors: operatorMutationErrorCodes("request.invalid", "resource.not_found", "mail.conflict", "mail.unavailable"),
			Handle: h.cancelDelivery,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/mail/deliveries/{mail_delivery_id}/retry",
			Auth:   route.AuthRecentSessionRequired,
			Errors: operatorMutationErrorCodes("request.invalid", "resource.not_found", "mail.conflict", "mail.unavailable"),
			Handle: h.retryDelivery,
		},
	)
}

func (h mailHandlers) getKeyState(request route.Request) (route.Result, error) {
	if h.app == nil {
		return route.Result{}, route.ErrUnpresentable
	}
	view, appErr := h.app.GetMailKeyState(request.Context, request.Invocation())
	if appErr != nil {
		return route.Result{}, appErr
	}
	response := mailKeyStateResponse{PrimaryKeyID: view.PrimaryKeyID,
		RequiredPrimaryKeyID: view.RequiredPrimaryKeyID,
		Active:               make([]mailPayloadKeyUsageResponse, 0, len(view.Active))}
	for _, usage := range view.Active {
		response.Active = append(response.Active, mailPayloadKeyUsageResponse{KeyID: usage.KeyID,
			ActiveReferences: usage.ActiveReferences})
	}
	return route.JSON(http.StatusOK, response), nil
}

func (h mailHandlers) startRekey(request route.Request) (route.Result, error) {
	if h.app == nil {
		return route.Result{}, route.ErrUnpresentable
	}

	var body mailRekeyRequest
	if err := request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	view, appErr := h.app.StartMailRekey(request.Context, request.Invocation(), body.RetiringKeyID)
	if appErr != nil {
		return route.Result{}, appErr
	}

	return route.JSON(http.StatusAccepted, mailRekeyResponse{
		JobID:         view.JobID.String(),
		PrimaryKeyID:  view.PrimaryKeyID,
		RetiringKeyID: view.RetiringKeyID,
		CreatedAt:     model.FormatRFC3339(view.CreatedAt),
	}), nil
}

func (h mailHandlers) getRekeyStatus(request route.Request) (route.Result, error) {
	if h.app == nil {
		return route.Result{}, route.ErrUnpresentable
	}

	raw, err := route.PathID[model.JobID](request, "job_id")
	if err != nil {
		return route.Result{}, err
	}

	view, appErr := h.app.GetMailRekeyStatus(request.Context, request.Invocation(), raw)
	if appErr != nil {
		return route.Result{}, appErr
	}

	response := mailRekeyStatusResponse{
		JobID:           view.JobID.String(),
		Status:          view.Status,
		PrimaryKeyID:    view.PrimaryKeyID,
		RetiringKeyID:   view.RetiringKeyID,
		CreatedAt:       model.FormatRFC3339(view.CreatedAt),
		UpdatedAt:       model.FormatRFC3339(view.UpdatedAt),
		PublicErrorCode: view.PublicErrorCode,
		AttemptCount:    view.AttemptCount,
		MaximumAttempts: view.MaximumAttempts,
		Processed:       view.Processed,
		Reencrypted:     view.Reencrypted,
	}
	if view.CompletedAt.Valid {
		response.CompletedAt = view.CompletedAt.FormatRFC3339()
	}
	if view.Progress != nil {
		response.Progress = &mailRekeyProgressResponse{Current: view.Progress.Current, Total: view.Progress.Total, Stage: view.Progress.Stage}
	}

	if view.Proof != nil {
		response.Proof = &mailRekeyProofResponse{
			NonPrimaryReferences: view.Proof.NonPrimaryReferences,
			RetiringReferences:   view.Proof.RetiringReferences,
			RetirementSafe:       view.Proof.RetirementSafe,
		}
	}

	return route.JSON(http.StatusOK, response), nil
}

func (h mailHandlers) getMetrics(request route.Request) (route.Result, error) {
	if h.app == nil {
		return route.Result{}, route.ErrUnpresentable
	}

	snapshot, appErr := h.app.GetMailMetrics(request.Context, request.Invocation())
	if appErr != nil {
		return route.Result{}, appErr
	}

	response := mailMetricsResponse{
		Deliveries: make([]mailDeliveryMetricResponse, 0, len(snapshot.Deliveries)),
		Queues:     make([]mailQueueMetricResponse, 0, len(snapshot.Queues)),
		HealthCode: snapshot.HealthCode,
	}
	for _, metric := range snapshot.Deliveries {
		response.Deliveries = append(response.Deliveries, mailDeliveryMetricResponse{
			TemplateKey:                    metric.TemplateKey,
			State:                          metric.State,
			OutcomeCode:                    metric.OutcomeCode,
			Count:                          metric.Count,
			AttemptCount:                   metric.AttemptCount,
			ProcessingLatencyMillis:        metric.ProcessingLatency.Milliseconds(),
			MaximumProcessingLatencyMillis: metric.MaximumProcessingLatency.Milliseconds(),
		})
	}

	for _, metric := range snapshot.Queues {
		response.Queues = append(response.Queues, mailQueueMetricResponse{
			TemplateKey:     metric.TemplateKey,
			State:           metric.State,
			OutcomeCode:     metric.OutcomeCode,
			Count:           metric.Count,
			OldestAgeMillis: metric.OldestAge.Milliseconds(),
			HealthCode:      metric.HealthCode,
			Truncated:       metric.Truncated,
		})
	}

	return route.JSON(http.StatusOK, response), nil
}

func (h mailHandlers) sendTest(request route.Request) (route.Result, error) {
	if h.app == nil {
		return route.Result{}, route.ErrUnpresentable
	}
	if request.HTTP.Body != nil {
		content, err := io.ReadAll(io.LimitReader(request.HTTP.Body, 1))
		if err != nil || len(content) != 0 {
			return route.Result{}, apperror.New("request.invalid")
		}
	}
	view, appErr := h.app.SendTestMail(request.Context, request.Invocation())
	if appErr != nil {
		return route.Result{}, appErr
	}
	return route.JSON(http.StatusAccepted, mailResponse(view)), nil
}

func (h mailHandlers) getDelivery(request route.Request) (route.Result, error) {
	if h.app == nil {
		return route.Result{}, route.ErrUnpresentable
	}
	raw, err := route.PathID[model.MailDeliveryID](request, "mail_delivery_id")
	if err != nil {
		return route.Result{}, err
	}
	view, appErr := h.app.GetMailDelivery(request.Context, request.Invocation(), raw)
	if appErr != nil {
		return route.Result{}, appErr
	}
	return route.JSON(http.StatusOK, mailResponse(view)), nil
}

func (h mailHandlers) listDeliveries(request route.Request) (route.Result, error) {
	if h.app == nil {
		return route.Result{}, route.ErrUnpresentable
	}

	query, err := listMailDeliveriesQuery(request.HTTP)
	if err != nil {
		return route.Result{}, apperror.Invalid("request.invalid", "query")
	}

	page, appErr := h.app.ListMailDeliveries(request.Context, request.Invocation(), query)
	if appErr != nil {
		return route.Result{}, appErr
	}

	response := mailDeliveryListResponse{Items: make([]mailDeliveryResponse, 0, len(page.Items))}
	for _, item := range page.Items {
		response.Items = append(response.Items, mailResponse(item))
	}

	if page.HasMore && len(page.Items) == 0 {
		return route.Result{}, route.Unpresentable(errors.New("mail delivery page made no progress"))
	}

	if page.HasMore {
		last := page.Items[len(page.Items)-1]
		response.NextCursor, err = encodeMailDeliveryCursor(mailDeliveryCursor{
			CreatedAt: model.FormatRFC3339(last.CreatedAt),
			ID:        last.ID.String(),
		})
		if err != nil {
			return route.Result{}, route.Unpresentable(err)
		}
	}

	if response.Total, response.TotalIsLowerBound, err = route.PageTotal(page.Total, !query.BeforeID.IsZero()); err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, response), nil
}

func (h mailHandlers) cancelDelivery(request route.Request) (route.Result, error) {
	if h.app == nil {
		return route.Result{}, route.ErrUnpresentable
	}
	return h.mutateDelivery(request, h.app.CancelMailDelivery)
}

func (h mailHandlers) retryDelivery(request route.Request) (route.Result, error) {
	if h.app == nil {
		return route.Result{}, route.ErrUnpresentable
	}
	return h.mutateDelivery(request, h.app.RetryMailDelivery)
}

func (h mailHandlers) mutateDelivery(
	request route.Request,
	mutate func(context.Context, invocation.Invocation, model.MailDeliveryID) (mail.DeliveryView, error),
) (route.Result, error) {
	if h.app == nil {
		return route.Result{}, route.ErrUnpresentable
	}
	if err := requireEmptyMailBody(request.HTTP); err != nil {
		return route.Result{}, err
	}

	raw, err := route.PathID[model.MailDeliveryID](request, "mail_delivery_id")
	if err != nil {
		return route.Result{}, err
	}

	view, appErr := mutate(request.Context, request.Invocation(), raw)
	if appErr != nil {
		return route.Result{}, appErr
	}

	return route.JSON(http.StatusOK, mailResponse(view)), nil
}

func requireEmptyMailBody(request *http.Request) error {
	if request.Body == nil {
		return nil
	}
	content, err := io.ReadAll(io.LimitReader(request.Body, 1))
	if err != nil || len(content) != 0 {
		return apperror.New("request.invalid")
	}
	return nil
}

func listMailDeliveriesQuery(request *http.Request) (mail.ListDeliveriesQuery, error) {
	page, err := route.ParsePage(request)
	if err != nil {
		return mail.ListDeliveriesQuery{}, err
	}
	query := mail.ListDeliveriesQuery{Limit: page.Limit}
	if raw := page.Cursor; raw != "" {
		var cursor mailDeliveryCursor
		if cursor, err = decodeMailDeliveryCursor(raw); err != nil {
			return query, err
		}

		query.BeforeCreatedAt, err = model.ParseRFC3339(cursor.CreatedAt)
		if err != nil {
			return query, errors.New("invalid mail delivery cursor time")
		}

		query.BeforeCreatedAt, query.BeforeID = query.BeforeCreatedAt.UTC(), model.MailDeliveryID(cursor.ID)
	}

	for _, raw := range request.URL.Query()["state"] {
		state := model.MailDeliveryState(raw)
		if !state.IsValid() {
			return query, errors.New("invalid mail delivery state")
		}

		query.States = append(query.States, state)
	}

	if len(query.States) > 6 {
		return query, errors.New("too many mail delivery states")
	}

	for _, raw := range request.URL.Query()["template_key"] {
		key := model.MailTemplateKey(raw)
		if !key.IsValid() {
			return query, errors.New("invalid mail template key")
		}

		query.TemplateKeys = append(query.TemplateKeys, key)
	}

	if len(query.TemplateKeys) > 64 {
		return query, errors.New("too many mail template keys")
	}

	if raw := request.URL.Query().Get("created_after"); raw != "" {
		query.CreatedAfter, err = parseMailFilterTime(raw)
		if err != nil {
			return query, err
		}
	}

	if raw := request.URL.Query().Get("created_before"); raw != "" {
		query.CreatedBefore, err = parseMailFilterTime(raw)
		if err != nil {
			return query, err
		}
	}

	if !query.CreatedAfter.IsZero() && !query.CreatedBefore.IsZero() && !query.CreatedAfter.Before(query.CreatedBefore) {
		return query, errors.New("invalid mail delivery time range")
	}

	return query, nil
}

func parseMailFilterTime(raw string) (time.Time, error) {
	parsed, err := model.ParseRFC3339(raw)
	if err != nil || parsed.IsZero() {
		return time.Time{}, errors.New("invalid mail delivery time")
	}
	return parsed, nil
}

func encodeMailDeliveryCursor(cursor mailDeliveryCursor) (string, error) {
	return route.EncodeCursor(cursor, mailDeliveryCursorSpec())
}

func decodeMailDeliveryCursor(raw string) (mailDeliveryCursor, error) {
	return route.DecodeCursor(raw, mailDeliveryCursorSpec())
}

func mailDeliveryCursorSpec() route.CursorSpec[mailDeliveryCursor] {
	return route.CursorSpec[mailDeliveryCursor]{
		Label:                "mail delivery",
		MaximumEncodedLength: route.DefaultMaximumCursorLength,
		Members:              []string{"created_at", "id"},
		Validate: func(cursor mailDeliveryCursor) error {
			parsed, err := model.ParseRFC3339(cursor.CreatedAt)
			if err != nil || parsed.IsZero() || !model.MailDeliveryID(cursor.ID).IsValid() {
				return errors.New("invalid mail delivery keyset")
			}

			return nil
		},
	}
}

func mailResponse(view mail.DeliveryView) mailDeliveryResponse {
	result := mailDeliveryResponse{
		ID:                 view.ID.String(),
		OccurrenceID:       view.OccurrenceID.String(),
		TargetUserID:       view.TargetUserID.String(),
		TargetInvitationID: view.TargetInvitationID.String(),
		TemplateKey:        view.TemplateKey,
		TemplateDigest:     view.TemplateDigest,
		MaskedRecipient:    view.MaskedRecipient,
		State:              view.State,
		CreatedAt:          model.FormatRFC3339(view.CreatedAt),
		UpdatedAt:          model.FormatRFC3339(view.UpdatedAt),
		MessageDate:        model.FormatRFC3339(view.MessageDate),
		Deadline:           model.FormatRFC3339(view.Deadline),
		MessageID:          view.MessageID,
		AttemptCount:       view.AttemptCount,
		PublicFailureCode:  view.PublicFailureCode,
	}
	if view.AcceptedAt.Valid {
		result.AcceptedAt = view.AcceptedAt.FormatRFC3339()
	}
	if view.FailedAt.Valid {
		result.FailedAt = view.FailedAt.FormatRFC3339()
	}

	return result
}

// MailApplication is the application port its handlers call; it reports on and operates transactional mail: keys, rekeying, test mail, metrics and deliveries.
type MailApplication interface {
	GetMailKeyState(context.Context, invocation.Invocation) (mail.KeyStateView, error)
	StartMailRekey(context.Context, invocation.Invocation, string) (mail.RekeyView, error)
	GetMailRekeyStatus(context.Context, invocation.Invocation, model.JobID) (mail.RekeyStatusView, error)
	SendTestMail(context.Context, invocation.Invocation) (mail.DeliveryView, error)
	GetMailMetrics(context.Context, invocation.Invocation) (mail.MetricsSnapshot, error)
	GetMailDelivery(context.Context, invocation.Invocation, model.MailDeliveryID) (mail.DeliveryView, error)
	ListMailDeliveries(context.Context, invocation.Invocation, mail.ListDeliveriesQuery) (mail.DeliveryPage, error)
	CancelMailDelivery(context.Context, invocation.Invocation, model.MailDeliveryID) (mail.DeliveryView, error)
	RetryMailDelivery(context.Context, invocation.Invocation, model.MailDeliveryID) (mail.DeliveryView, error)
}
