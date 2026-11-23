// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package review

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// LiveEvidenceQuery names one attempt of a sitting and the window and page
// of a live evidence read. FlagID is for the evidence read only; the
// keyset members that apply depend on the read.
type LiveEvidenceQuery struct {
	ExamID        model.ExamID
	SittingID     model.ExamSittingID
	AttemptID     model.ExamAttemptID
	FlagID        model.IntegrityFlagID
	Window        store.LiveEvidenceWindow
	AfterID       string
	AfterTimeline *store.LiveTimelineCursor
	Limit         int
}

// LiveEvidenceRead is what every live read returns besides its page:
// Complete once the attempt is sealed, so nothing more of its own arrives,
// and the database time the page is current as of.
type LiveEvidenceRead struct {
	Complete bool
	AsOf     time.Time
}

// LiveFlagPage is one page of an attempt's flags.
type LiveFlagPage struct {
	LiveEvidenceRead
	store.ExamIntegrityFlagPage
}

// LiveEvidencePage is one page of one flag's evidence.
type LiveEvidencePage struct {
	LiveEvidenceRead
	store.ExamIntegrityEvidencePage
}

// LiveNativeConditionPage is one page of an attempt's native conditions.
type LiveNativeConditionPage struct {
	LiveEvidenceRead
	store.NativeConditionPage
}

// LiveTimelinePage is one page of an attempt's merged timeline.
type LiveTimelinePage struct {
	LiveEvidenceRead
	store.LiveTimelinePage
}

// ListLiveFlags is the live, attempt-scoped form of the Submission's flag
// read, for the sitting's managers while the attempt runs.
func (s *Service) ListLiveFlags(ctx context.Context, inv invocation.Invocation, query LiveEvidenceQuery) (_ LiveFlagPage, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	var after model.IntegrityFlagID
	if query.AfterID != "" {
		after = model.IntegrityFlagID(query.AfterID)
	}
	read, err := s.authorizeLiveEvidence(ctx, inv, query, !after.IsZero() && !after.IsValid())
	if err != nil {
		return LiveFlagPage{}, err
	}
	page, err := s.reviews.ListLiveFlags(ctx, store.LiveFlagListOptions{AttemptID: query.AttemptID, Window: query.Window,
		AfterFlagID: after, Limit: query.Limit})
	if err != nil {
		return LiveFlagPage{}, storeErrors.Translate(err)
	}
	if page == nil || len(page.Items) > query.Limit || page.HasMore && len(page.Items) != query.Limit {
		return LiveFlagPage{}, apperror.New("exam.integrity_review.unavailable").Wrap(errors.New("inconsistent live Flag page"))
	}
	for _, item := range page.Items {
		if item.Flag.AttemptID != query.AttemptID || item.Browser != nil && item.Browser.Validate() != nil {
			return LiveFlagPage{}, apperror.New("exam.integrity_review.unavailable").Wrap(errors.New("live Flag outside its attempt"))
		}
	}
	return LiveFlagPage{LiveEvidenceRead: read, ExamIntegrityFlagPage: store.ExamIntegrityFlagPage{
		Items: slices.Clone(page.Items), HasMore: page.HasMore}}, nil
}

// ListLiveEvidence is the live form of a flag's evidence read.
func (s *Service) ListLiveEvidence(ctx context.Context, inv invocation.Invocation, query LiveEvidenceQuery) (_ LiveEvidencePage, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	after := model.IntegrityEvidenceID(query.AfterID)
	read, err := s.authorizeLiveEvidence(ctx, inv, query, !query.FlagID.IsValid() || query.AfterID != "" && !after.IsValid())
	if err != nil {
		return LiveEvidencePage{}, err
	}
	page, err := s.reviews.ListLiveEvidence(ctx, store.LiveEvidenceListOptions{AttemptID: query.AttemptID, FlagID: query.FlagID,
		Window: query.Window, AfterEvidenceID: after, Limit: query.Limit})
	if err != nil {
		return LiveEvidencePage{}, storeErrors.Translate(err)
	}
	if page == nil || len(page.Items) > query.Limit || page.HasMore && len(page.Items) != query.Limit {
		return LiveEvidencePage{}, apperror.New("exam.integrity_review.unavailable").Wrap(errors.New("inconsistent live Evidence page"))
	}
	items := slices.Clone(page.Items)
	for i := range items {
		if items[i].AttemptID != query.AttemptID || items[i].FlagID != query.FlagID {
			return LiveEvidencePage{}, apperror.New("exam.integrity_review.unavailable").Wrap(errors.New("live Evidence outside its Flag"))
		}
		if items[i].Browser != nil {
			value := items[i].Browser.Clone()
			items[i].Browser = &value
		}
	}
	return LiveEvidencePage{LiveEvidenceRead: read, ExamIntegrityEvidencePage: store.ExamIntegrityEvidencePage{
		Items: items, HasMore: page.HasMore}}, nil
}

// ListLiveNativeConditions is the live form of the native condition read.
func (s *Service) ListLiveNativeConditions(
	ctx context.Context,
	inv invocation.Invocation,
	query LiveEvidenceQuery,
) (_ LiveNativeConditionPage, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	after := model.IntegrityEvidenceID(query.AfterID)
	read, err := s.authorizeLiveEvidence(ctx, inv, query, query.AfterID != "" && !after.IsValid())
	if err != nil {
		return LiveNativeConditionPage{}, err
	}
	page, err := s.reviews.ListLiveNativeConditions(ctx, store.LiveNativeConditionListOptions{AttemptID: query.AttemptID,
		Window: query.Window, AfterID: after, Limit: query.Limit})
	if err != nil {
		return LiveNativeConditionPage{}, storeErrors.Translate(err)
	}
	if page == nil || page.Items == nil || len(page.Items) > query.Limit || page.HasMore && len(page.Items) != query.Limit {
		return LiveNativeConditionPage{}, apperror.New("exam.integrity_review.unavailable").Wrap(errors.New("inconsistent live native condition page"))
	}
	items := make([]model.NativeConditionEvidence, len(page.Items))
	for i, value := range page.Items {
		if value.Validate() != nil || value.AttemptID != query.AttemptID {
			return LiveNativeConditionPage{}, apperror.New("exam.integrity_review.unavailable").Wrap(model.ErrNativeDeliveryInvalid)
		}
		items[i] = value.Clone()
	}
	return LiveNativeConditionPage{LiveEvidenceRead: read, NativeConditionPage: store.NativeConditionPage{
		Items: items, HasMore: page.HasMore}}, nil
}

// ListLiveTimeline merges an attempt's flags, evidence and native conditions
// in time order.
func (s *Service) ListLiveTimeline(ctx context.Context, inv invocation.Invocation, query LiveEvidenceQuery) (_ LiveTimelinePage, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	read, err := s.authorizeLiveEvidence(ctx, inv, query, query.AfterID != "")
	if err != nil {
		return LiveTimelinePage{}, err
	}
	page, err := s.reviews.ListLiveTimeline(ctx, store.LiveTimelineListOptions{AttemptID: query.AttemptID, Window: query.Window,
		After: query.AfterTimeline, Limit: query.Limit})
	if err != nil {
		return LiveTimelinePage{}, storeErrors.Translate(err)
	}
	if page == nil || page.Items == nil || len(page.Items) > query.Limit || page.HasMore && len(page.Items) != query.Limit {
		return LiveTimelinePage{}, apperror.New("exam.integrity_review.unavailable").Wrap(errors.New("inconsistent live timeline page"))
	}
	items := slices.Clone(page.Items)
	for i := range items {
		item := &items[i]
		switch {
		case item.Kind == store.LiveTimelineFlag && item.Flag != nil && item.Flag.Flag.AttemptID == query.AttemptID:
			value := *item.Flag
			item.Flag = &value
		case item.Kind == store.LiveTimelineEvidence && item.Evidence != nil && item.Evidence.AttemptID == query.AttemptID:
			value := *item.Evidence
			if value.Browser != nil {
				browser := value.Browser.Clone()
				value.Browser = &browser
			}
			item.Evidence = &value
		case item.Kind == store.LiveTimelineNativeCondition && item.NativeCondition != nil &&
			item.NativeCondition.AttemptID == query.AttemptID && item.NativeCondition.Validate() == nil:
			value := item.NativeCondition.Clone()
			item.NativeCondition = &value
		default:
			return LiveTimelinePage{}, apperror.New("exam.integrity_review.unavailable").Wrap(errors.New("invalid live timeline entry"))
		}
	}
	return LiveTimelinePage{LiveEvidenceRead: read, LiveTimelinePage: store.LiveTimelinePage{Items: items, HasMore: page.HasMore}}, nil
}

// authorizeLiveEvidence validates the query, authorizes the sitting, then
// checks the attempt is the sitting's and not the caller's own; the caller
// never reads evidence about themself, and that refusal is recorded as it is
// for a Submission.
func (s *Service) authorizeLiveEvidence(
	ctx context.Context,
	inv invocation.Invocation,
	query LiveEvidenceQuery,
	invalidKeyset bool,
) (LiveEvidenceRead, error) {
	if !query.ExamID.IsValid() || !query.SittingID.IsValid() || !query.AttemptID.IsValid() || invalidKeyset ||
		query.Limit < 1 || query.Limit > store.LiveEvidenceReadMaximum ||
		!query.Window.From.IsZero() && !query.Window.To.IsZero() && !query.Window.From.Before(query.Window.To) {
		return LiveEvidenceRead{}, apperror.Invalid("exam.integrity_review.invalid", "live_evidence")
	}
	if s.authorizer.Sittings == nil || s.authorizer.Audit == nil {
		return LiveEvidenceRead{}, apperror.New("exam.integrity_review.unavailable")
	}
	unitID, err := s.authorizer.Sittings.AuthorizeLiveEvidence(ctx, inv, query.ExamID, query.SittingID)
	if err != nil {
		return LiveEvidenceRead{}, err
	}
	scope, err := s.reviews.ResolveLiveEvidence(ctx, query.AttemptID)
	if err != nil {
		return LiveEvidenceRead{}, storeErrors.Translate(err)
	}
	if scope.AttemptID != query.AttemptID || scope.ExamID != query.ExamID || scope.SittingID != query.SittingID {
		return LiveEvidenceRead{}, apperror.New("exam.integrity_review.not_found")
	}
	allowed := scope.CandidateUserID != inv.Principal().UserID
	if err = s.authorizer.Audit.RecordAttemptEvidenceDecision(ctx, inv, scope.AttemptID,
		scope.CandidateUserID, unitID, query.FlagID, allowed); err != nil {
		return LiveEvidenceRead{}, err
	}
	if !allowed {
		return LiveEvidenceRead{}, apperror.New("exam.integrity_review.not_found")
	}
	return LiveEvidenceRead{Complete: scope.Submitted || scope.Retired, AsOf: scope.AsOf}, nil
}
