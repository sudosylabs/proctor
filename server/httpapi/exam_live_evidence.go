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

	"github.com/sudosylabs/proctor/server/app/exam/review"
	"github.com/sudosylabs/proctor/server/app/people"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// liveEvidenceErrors are what every live evidence read can answer.
var liveEvidenceErrors = []string{
	"authorization.request.invalid",
	"authorization.unavailable",
	"request.invalid",
	"resource.not_found",
	"exam.integrity_review.invalid",
	"exam.integrity_review.unavailable",
	"exam.sitting.invalid",
	"exam.sitting.unavailable",
}

// examLiveEvidenceResource serves a running attempt's evidence to the
// sitting's managers, read-only, in the shapes the Submission's reads use.
func examLiveEvidenceResource(app *review.Service, resolver *people.Resolver) route.Resource {
	h := examIntegrityReviewHandlers{app: app, people: resolver}
	base := "/exams/{exam_id}/sittings/{exam_sitting_id}/attempts/{exam_attempt_id}"
	endpoint := func(path string, handle func(route.Request) (route.Result, error)) route.Endpoint {
		return route.Endpoint{Method: http.MethodGet, Path: base + path, Auth: route.AuthPrincipalRequired,
			Errors: liveEvidenceErrors, Handle: handle}
	}
	return route.NewResource("exam-live-evidence",
		endpoint("/integrity-flags", h.listLiveFlags),
		endpoint("/integrity-flags/{integrity_flag_id}/evidence", h.listLiveEvidence),
		endpoint("/native-conditions", h.listLiveNativeConditions),
		endpoint("/timeline", h.listLiveTimeline),
	)
}

type liveFlagListResponse struct {
	Complete   bool                        `json:"complete"`
	AsOf       string                      `json:"as_of"`
	Items      []examIntegrityFlagResponse `json:"items"`
	NextCursor string                      `json:"next_cursor,omitempty"`
}

type liveEvidenceListResponse struct {
	Complete   bool                            `json:"complete"`
	AsOf       string                          `json:"as_of"`
	Items      []examIntegrityEvidenceResponse `json:"items"`
	NextCursor string                          `json:"next_cursor,omitempty"`
}

type liveNativeConditionListResponse struct {
	Complete   bool                            `json:"complete"`
	AsOf       string                          `json:"as_of"`
	Items      []model.NativeConditionEvidence `json:"items"`
	NextCursor string                          `json:"next_cursor,omitempty"`
}

// liveTimelineItemResponse is one timeline entry; exactly the member its
// kind names is present.
type liveTimelineItemResponse struct {
	Kind            string                         `json:"kind"`
	At              string                         `json:"at"`
	Flag            *examIntegrityFlagResponse     `json:"flag,omitempty"`
	Evidence        *examIntegrityEvidenceResponse `json:"evidence,omitempty"`
	NativeCondition *model.NativeConditionEvidence `json:"native_condition,omitempty"`
}

type liveTimelineListResponse struct {
	Complete   bool                       `json:"complete"`
	AsOf       string                     `json:"as_of"`
	Items      []liveTimelineItemResponse `json:"items"`
	NextCursor string                     `json:"next_cursor,omitempty"`
}

const liveTimelineCursorKind = "live_timeline"

type liveTimelineCursor struct {
	Kind      string `json:"kind"`
	At        string `json:"at"`
	EntryKind string `json:"entry_kind"`
	ID        string `json:"id"`
}

func liveTimelineCursorSpec() route.CursorSpec[liveTimelineCursor] {
	return route.CursorSpec[liveTimelineCursor]{
		Label:                "Attempt timeline",
		MaximumEncodedLength: route.DefaultMaximumCursorLength,
		Members:              []string{"kind", "at", "entry_kind", "id"},
		Validate: func(cursor liveTimelineCursor) error {
			kind := store.LiveTimelineKind(cursor.EntryKind)
			if _, err := model.ParseRFC3339(cursor.At); err != nil || cursor.Kind != liveTimelineCursorKind ||
				!model.IsValidOpaqueID(cursor.ID) ||
				kind != store.LiveTimelineFlag && kind != store.LiveTimelineEvidence && kind != store.LiveTimelineNativeCondition {
				return errors.New("invalid Attempt timeline keyset")
			}
			return nil
		},
	}
}

// liveEvidenceQuery reads the path, the from/to window and the page. A plain
// ID cursor is decoded for kind; the timeline decodes its own.
func liveEvidenceQuery(request route.Request, kind string) (review.LiveEvidenceQuery, error) {
	examID, sittingID, attemptID, err := managedExamAttemptIDs(request)
	if err != nil {
		return review.LiveEvidenceQuery{}, err
	}
	page, err := request.Page()
	if err != nil {
		return review.LiveEvidenceQuery{}, err
	}
	query := review.LiveEvidenceQuery{ExamID: examID, SittingID: sittingID, AttemptID: attemptID, Limit: page.Limit}
	values := request.HTTP.URL.Query()
	for name, bound := range map[string]*time.Time{"from": &query.Window.From, "to": &query.Window.To} {
		if raw := values.Get(name); raw != "" {
			at, parseErr := model.ParseRFC3339(raw)
			if parseErr != nil {
				return query, route.InvalidRequest(errors.New(name + " must be an RFC 3339 instant"))
			}
			*bound = at
		}
	}
	if raw := page.Cursor; raw != "" {
		if kind == "timeline" {
			cursor, decodeErr := route.DecodeCursor(raw, liveTimelineCursorSpec())
			if decodeErr != nil {
				return query, route.InvalidRequest(decodeErr)
			}
			at, _ := model.ParseRFC3339(cursor.At)
			query.AfterTimeline = &store.LiveTimelineCursor{At: at, Kind: store.LiveTimelineKind(cursor.EntryKind), ID: cursor.ID}
		} else {
			id, decodeErr := decodeExamIntegrityReviewCursor(raw, kind)
			if decodeErr != nil {
				return query, route.InvalidRequest(decodeErr)
			}
			query.AfterID = id
		}
	}
	return query, nil
}

func (h examIntegrityReviewHandlers) listLiveFlags(request route.Request) (route.Result, error) {
	query, err := liveEvidenceQuery(request, "live_flag")
	if err != nil {
		return route.Result{}, err
	}
	page, err := h.app.ListLiveFlags(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}
	response := liveFlagListResponse{Complete: page.Complete, AsOf: model.FormatRFC3339(page.AsOf),
		Items: make([]examIntegrityFlagResponse, 0, len(page.Items))}
	for _, item := range page.Items {
		response.Items = append(response.Items, examIntegrityFlagResponseFrom(request.HTTP, item))
	}
	if page.HasMore {
		if response.NextCursor, err = encodeExamIntegrityReviewCursor("live_flag", page.Items[len(page.Items)-1].Flag.ID.String()); err != nil {
			return route.Result{}, route.Unpresentable(err)
		}
	}
	return route.JSON(http.StatusOK, response).WithHeaders(noStoreHeaders()), nil
}

func (h examIntegrityReviewHandlers) listLiveEvidence(request route.Request) (route.Result, error) {
	query, err := liveEvidenceQuery(request, "live_evidence")
	if err != nil {
		return route.Result{}, err
	}
	if query.FlagID, err = reviewFlagID(request); err != nil {
		return route.Result{}, err
	}
	page, err := h.app.ListLiveEvidence(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}
	response := liveEvidenceListResponse{Complete: page.Complete, AsOf: model.FormatRFC3339(page.AsOf),
		Items: make([]examIntegrityEvidenceResponse, 0, len(page.Items))}
	for _, item := range page.Items {
		response.Items = append(response.Items, examIntegrityEvidenceResponseFrom(item))
	}
	if page.HasMore {
		if response.NextCursor, err = encodeExamIntegrityReviewCursor("live_evidence", page.Items[len(page.Items)-1].ID.String()); err != nil {
			return route.Result{}, route.Unpresentable(err)
		}
	}
	return route.JSON(http.StatusOK, response).WithHeaders(noStoreHeaders()), nil
}

func (h examIntegrityReviewHandlers) listLiveNativeConditions(request route.Request) (route.Result, error) {
	query, err := liveEvidenceQuery(request, "live_native_condition")
	if err != nil {
		return route.Result{}, err
	}
	page, err := h.app.ListLiveNativeConditions(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}
	response := liveNativeConditionListResponse{Complete: page.Complete, AsOf: model.FormatRFC3339(page.AsOf), Items: page.Items}
	if page.HasMore {
		if response.NextCursor, err = encodeExamIntegrityReviewCursor("live_native_condition", page.Items[len(page.Items)-1].ID.String()); err != nil {
			return route.Result{}, route.Unpresentable(err)
		}
	}
	return route.JSON(http.StatusOK, response).WithHeaders(noStoreHeaders()), nil
}

func (h examIntegrityReviewHandlers) listLiveTimeline(request route.Request) (route.Result, error) {
	query, err := liveEvidenceQuery(request, "timeline")
	if err != nil {
		return route.Result{}, err
	}
	page, err := h.app.ListLiveTimeline(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}
	response := liveTimelineListResponse{Complete: page.Complete, AsOf: model.FormatRFC3339(page.AsOf),
		Items: make([]liveTimelineItemResponse, 0, len(page.Items))}
	for _, item := range page.Items {
		entry := liveTimelineItemResponse{Kind: string(item.Kind), At: model.FormatRFC3339(item.At), NativeCondition: item.NativeCondition}
		if item.Flag != nil {
			flag := examIntegrityFlagResponseFrom(request.HTTP, *item.Flag)
			entry.Flag = &flag
		}
		if item.Evidence != nil {
			evidence := examIntegrityEvidenceResponseFrom(*item.Evidence)
			entry.Evidence = &evidence
		}
		response.Items = append(response.Items, entry)
	}
	if page.HasMore {
		last := page.Items[len(page.Items)-1]
		cursor := liveTimelineCursor{Kind: liveTimelineCursorKind, At: model.FormatRFC3339(last.At), EntryKind: string(last.Kind)}
		switch {
		case last.Flag != nil:
			cursor.ID = last.Flag.Flag.ID.String()
		case last.Evidence != nil:
			cursor.ID = last.Evidence.ID.String()
		case last.NativeCondition != nil:
			cursor.ID = last.NativeCondition.ID.String()
		}
		if response.NextCursor, err = route.EncodeCursor(cursor, liveTimelineCursorSpec()); err != nil {
			return route.Result{}, route.Unpresentable(err)
		}
	}
	return route.JSON(http.StatusOK, response).WithHeaders(noStoreHeaders()), nil
}
