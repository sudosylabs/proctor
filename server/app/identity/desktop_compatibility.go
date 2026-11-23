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
	"regexp"
	"slices"
	"sort"
	"strconv"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// DesktopCompatibilityState is the closed set of results of checking a
// declared desktop build against this server and the institution policy.
type DesktopCompatibilityState string

// The compatibility states. UpdateRequired means the desktop app is too old,
// ServerIncompatible that it is newer than this server supports, and
// UnsupportedTarget that no build exists for its platform and architecture.
const (
	DesktopCompatibilityCompatible         DesktopCompatibilityState = "compatible"
	DesktopCompatibilityUpdateRequired     DesktopCompatibilityState = "update_required"
	DesktopCompatibilityServerIncompatible DesktopCompatibilityState = "server_incompatible"
	DesktopCompatibilityUnsupportedTarget  DesktopCompatibilityState = "unsupported_target"
)

// DesktopCompatibilityQuery is untrusted, client-declared build metadata.
type DesktopCompatibilityQuery struct {
	DesktopRelease   string
	DesktopBuildID   string
	Platform         string
	Architecture     string
	RealtimeProtocol int
}

// DesktopCompatibilityResult is safe to return to clients, except
// PolicyRevision.
type DesktopCompatibilityResult struct {
	Availability            model.DesktopAvailability
	RetryAt                 model.OptionalTime
	Compatibility           DesktopCompatibilityState
	Reason                  string
	MinimumDesktopRelease   string
	MaximumDesktopRelease   string
	MinimumRealtimeProtocol int
	MaximumRealtimeProtocol int
	AdministratorMessage    string
	// PolicyRevision is an internal admission fence carried back to the store;
	// transports must not expose it.
	PolicyRevision int64
}

type desktopBuildCatalog struct {
	entries []model.DesktopBuildTuple
}

// ReplaceDesktopCompatibilityPolicyCommand replaces the whole policy;
// ExpectedRevision and IdempotencyKey are both required.
type ReplaceDesktopCompatibilityPolicyCommand struct {
	ExpectedRevision int64
	Settings         model.DesktopCompatibilityPolicySettings
	IdempotencyKey   string
}

var validDesktopTargetSelector = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

func newDesktopBuildCatalog(entries []model.DesktopBuildTuple) (*desktopBuildCatalog, error) {
	cloned := slices.Clone(entries)
	seenBuildIDs := make(map[string]struct{}, len(cloned))
	for _, entry := range cloned {
		if err := entry.Validate(); err != nil {
			return nil, err
		}
		if _, exists := seenBuildIDs[entry.DesktopBuildID]; exists {
			return nil, errors.New("desktop build catalog contains a duplicate build ID")
		}
		seenBuildIDs[entry.DesktopBuildID] = struct{}{}
	}
	sort.Slice(cloned, func(left, right int) bool {
		first, second := cloned[left], cloned[right]
		if first.Platform != second.Platform {
			return first.Platform < second.Platform
		}
		if first.Architecture != second.Architecture {
			return first.Architecture < second.Architecture
		}
		comparison, _ := model.CompareDesktopReleases(first.DesktopRelease, second.DesktopRelease)
		if comparison != 0 {
			return comparison < 0
		}
		return first.DesktopBuildID < second.DesktopBuildID
	})
	return &desktopBuildCatalog{entries: cloned}, nil
}

// requireDesktopCompatibility also builds the immutable build catalog, so New
// calls it before anything that evaluates builds.
func (s *Service) requireDesktopCompatibility(builds []model.DesktopBuildTuple) error {
	if s.desktopCompatibilityPolicies == nil ||
		s.institutions == nil ||
		s.authorizer == nil ||
		s.recentAuthenticationTTL <= 0 ||
		s.now == nil {
		return errors.New("desktop compatibility service dependencies are invalid")
	}

	catalog, err := newDesktopBuildCatalog(builds)
	if err != nil {
		return err
	}

	s.desktopBuilds = catalog
	return nil
}

// EvaluateDesktopCompatibility checks an untrusted build declaration against
// the current institution policy and the server's build catalog.
func (s *Service) EvaluateDesktopCompatibility(
	ctx context.Context,
	_ invocation.Invocation,
	query DesktopCompatibilityQuery,
) (DesktopCompatibilityResult, error) {
	return s.evaluateDesktopBuild(ctx, query, s.skipDesktopBuildChecks)
}

func (s *Service) evaluateDesktopBuild(
	ctx context.Context,
	query DesktopCompatibilityQuery,
	skipBuildChecks bool,
) (DesktopCompatibilityResult, error) {
	if err := validateDesktopCompatibilityQuery(query); err != nil {
		return DesktopCompatibilityResult{}, err
	}

	policy, err := s.desktopCompatibilityPolicies.Get(ctx)
	if err != nil || policy == nil {
		return DesktopCompatibilityResult{}, desktopCompatibilityPolicyError(err)
	}

	var result DesktopCompatibilityResult
	if skipBuildChecks {
		if policy.Validate() != nil {
			return DesktopCompatibilityResult{}, apperror.New("desktop_compatibility_policy.unavailable")
		}

		result = DesktopCompatibilityResult{
			Availability:         policy.Availability,
			RetryAt:              policy.RetryAt,
			Compatibility:        DesktopCompatibilityCompatible,
			Reason:               "compatible",
			AdministratorMessage: policy.AdministratorMessage,
		}
	} else {
		result, err = s.desktopBuilds.evaluate(policy, query)
	}

	if err != nil {
		return DesktopCompatibilityResult{}, err
	}
	if result.RetryAt.Valid && !result.RetryAt.Time.After(model.TimeUTC(s.now())) {
		result.RetryAt = model.OptionalTime{}
	}

	result.PolicyRevision = policy.Revision
	return result, nil
}

// ResolveAttemptDesktopBuild returns the catalog build the desktop session was
// established with, taken from the principal rather than the request body.
// Policy may have narrowed the catalog since, so it is rechecked at entry.
func (s *Service) ResolveAttemptDesktopBuild(
	ctx context.Context,
	principal model.Principal,
) (attempt.DesktopBuildResolution, error) {
	if principal.Validate() != nil || !principal.HasRegisteredDesktopKey() {
		return attempt.DesktopBuildResolution{}, errors.New("Desktop principal is invalid")
	}

	query := DesktopCompatibilityQuery{
		DesktopRelease:   principal.DesktopRelease,
		DesktopBuildID:   principal.DesktopBuildID,
		Platform:         string(principal.DesktopPlatform),
		Architecture:     string(principal.DesktopArchitecture),
		RealtimeProtocol: principal.DesktopRealtimeProtocol,
	}
	result, err := s.evaluateDesktopBuild(ctx, query, false)
	if err != nil ||
		result.Availability != model.DesktopAvailabilityReady ||
		result.Compatibility != DesktopCompatibilityCompatible {
		return attempt.DesktopBuildResolution{}, errors.New("Desktop build is not compatible")
	}

	for _, entry := range s.desktopBuilds.entries {
		if entry.DesktopRelease == query.DesktopRelease &&
			entry.DesktopBuildID == query.DesktopBuildID &&
			entry.Platform == principal.DesktopPlatform &&
			entry.Architecture == principal.DesktopArchitecture &&
			entry.RealtimeProtocol == query.RealtimeProtocol {
			return attempt.DesktopBuildResolution{Build: entry, CompatibilityPolicyRevision: result.PolicyRevision}, nil
		}
	}

	return attempt.DesktopBuildResolution{}, errors.New("Desktop build is missing from the verified catalog")
}

// AssessDesktopBuilds reports, for each build, whether the verified catalog
// and the compatibility policy would admit it to an Attempt now. The policy is
// read once for the whole list. Availability is not considered: it is the
// installation's state, not the build's.
func (s *Service) AssessDesktopBuilds(ctx context.Context, builds []model.DesktopBuildTuple) ([]bool, error) {
	admitted := make([]bool, len(builds))
	if len(builds) == 0 {
		return admitted, nil
	}

	policy, err := s.desktopCompatibilityPolicies.Get(ctx)
	if err != nil || policy == nil {
		return nil, desktopCompatibilityPolicyError(err)
	}

	for index, build := range builds {
		result, evaluateErr := s.desktopBuilds.evaluate(policy, DesktopCompatibilityQuery{
			DesktopRelease:   build.DesktopRelease,
			DesktopBuildID:   build.DesktopBuildID,
			Platform:         string(build.Platform),
			Architecture:     string(build.Architecture),
			RealtimeProtocol: build.RealtimeProtocol,
		})
		// A registration whose recorded build is malformed is not admitted;
		// it is not a fault of the read.
		admitted[index] = evaluateErr == nil && result.Compatibility == DesktopCompatibilityCompatible
	}
	return admitted, nil
}

// GetDesktopCompatibilityPolicy returns a copy of the current policy.
func (s *Service) GetDesktopCompatibilityPolicy(
	ctx context.Context,
	inv invocation.Invocation,
) (*DesktopCompatibilityPolicyView, error) {
	if _, _, err := s.authorizeInstitutionAction(ctx, inv, model.ActionAccessPolicyView); err != nil {
		return nil, err
	}
	policy, err := s.desktopCompatibilityPolicies.Get(ctx)
	if err != nil || policy == nil {
		return nil, desktopCompatibilityPolicyError(err)
	}
	return s.describePolicy(policy.Clone()), nil
}

// ReplaceDesktopCompatibilityPolicy replaces the current policy; it requires
// strong recent authentication and fails on a stale revision.
func (s *Service) ReplaceDesktopCompatibilityPolicy(
	ctx context.Context,
	inv invocation.Invocation,
	command ReplaceDesktopCompatibilityPolicyCommand,
) (*DesktopCompatibilityPolicyView, error) {
	if err := access.RequireStrongRecentSession(
		inv.Principal(),
		s.now(),
		s.recentAuthenticationTTL,
	); err != nil {
		return nil, err
	}

	institution, resource, err := s.authorizeInstitutionAction(
		ctx,
		inv,
		model.ActionDesktopCompatibilityPolicyManage,
	)
	if err != nil {
		return nil, err
	}
	if command.ExpectedRevision < 1 || command.Settings.Validate() != nil {
		return nil, apperror.New("desktop_compatibility_policy.invalid")
	}
	if command.IdempotencyKey == "" {
		return nil, apperror.New("idempotency.key_required")
	}

	commandIdempotency, err := idempotency.Prepare(
		inv,
		"desktop_compatibility_policy.replace",
		command.IdempotencyKey,
		struct {
			ExpectedRevision int64                                    `json:"expected_revision"`
			Settings         model.DesktopCompatibilityPolicySettings `json:"settings"`
		}{
			ExpectedRevision: command.ExpectedRevision,
			Settings:         command.Settings,
		},
	)
	if err != nil {
		return nil, err
	}

	stored, err := access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: inv,
			Action:     model.ActionDesktopCompatibilityPolicyManage,
			Resource:   resource,
			ScopeType:  model.RoleScopeInstitution,
			ScopeID:    institution.ID.String(),
			Operation:  "replace",
			Value: map[string]any{
				"expected_revision":         command.ExpectedRevision,
				"minimum_desktop_release":   command.Settings.MinimumDesktopRelease,
				"revoked_build_count":       len(command.Settings.RevokedDesktopBuilds),
				"administrator_message_set": command.Settings.AdministratorMessage != "",
				"availability":              command.Settings.Availability,
				"retry_at_set":              command.Settings.RetryAt.Valid,
			},
			At: s.now(),
		},
		func(
			ctx context.Context,
			reference access.MutationReference,
		) (*store.DesktopCompatibilityPolicyReplacementResult, error) {
			return s.desktopCompatibilityPolicies.Replace(
				ctx,
				&store.DesktopCompatibilityPolicyReplacement{
					ActorID:          inv.Principal().UserID,
					ExpectedRevision: command.ExpectedRevision,
					Settings:         command.Settings,
					AuditEventID:     reference.ID,
					AuditAt:          reference.MutationAt,
				},
				commandIdempotency,
			)
		},
		desktopCompatibilityPolicyError,
	)
	if err != nil {
		return nil, err
	}
	if stored == nil || stored.Policy == nil || stored.Policy.Validate() != nil {
		return nil, apperror.New("desktop_compatibility_policy.unavailable")
	}
	if stored.Changed && !stored.Replayed && s.publisher != nil {
		event, eventErr := realtime.NewDesktopCompatibilityPolicyChangedEvent(institution.ID)
		if eventErr == nil {
			eventErr = s.publisher.Publish(ctx, event)
		}
		if eventErr != nil {
			s.publisher.Report(ctx, "desktop_compatibility_policy.changed", eventErr)
		}
	}

	return s.describePolicy(stored.Policy.Clone()), nil
}

func (s *Service) authorizeInstitutionAction(
	ctx context.Context,
	inv invocation.Invocation,
	action model.Action,
) (*model.Institution, model.Resource, error) {
	institution, err := s.institutions.GetSingleton(ctx)
	if err != nil || institution == nil || !institution.ID.IsValid() {
		return nil, model.Resource{}, desktopCompatibilityPolicyStoreErrors.Translate(err)
	}
	resource := model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()}
	if err := s.authorizer.Authorize(ctx, inv, action, resource); err != nil {
		return nil, model.Resource{}, err
	}
	return institution, resource, nil
}

func desktopCompatibilityPolicyError(err error) error {
	var revision *store.ErrDesktopCompatibilityPolicyRevisionConflict
	if errors.As(err, &revision) {
		return apperror.New("desktop_compatibility_policy.revision_conflict").WithField(
			"current_revision",
			strconv.FormatInt(revision.CurrentRevision, 10),
		).Wrap(err)
	}
	return desktopCompatibilityPolicyStoreErrors.Translate(err)
}

var desktopCompatibilityPolicyStoreErrors = apperror.StoreTable{
	Constraints: map[string]apperror.Code{"actor_not_system_administrator": "authorization.denied"},
	Unavailable: "desktop_compatibility_policy.unavailable",
}

func (c *desktopBuildCatalog) evaluate(
	policy *model.DesktopCompatibilityPolicy,
	query DesktopCompatibilityQuery,
) (DesktopCompatibilityResult, error) {
	if err := validateDesktopCompatibilityQuery(query); err != nil {
		return DesktopCompatibilityResult{}, err
	}
	if c == nil || policy == nil || policy.Validate() != nil {
		return DesktopCompatibilityResult{}, apperror.New("desktop_compatibility_policy.unavailable")
	}

	matchingTarget := make([]model.DesktopBuildTuple, 0, len(c.entries))
	for _, entry := range c.entries {
		if string(entry.Platform) == query.Platform && string(entry.Architecture) == query.Architecture {
			matchingTarget = append(matchingTarget, entry)
		}
	}

	if len(matchingTarget) == 0 {
		return DesktopCompatibilityResult{
			Availability:         policy.Availability,
			RetryAt:              policy.RetryAt,
			Compatibility:        DesktopCompatibilityUnsupportedTarget,
			Reason:               "unsupported_target",
			AdministratorMessage: policy.AdministratorMessage,
		}, nil
	}

	result := desktopCompatibilityBounds(policy, matchingTarget)
	result.AdministratorMessage = policy.AdministratorMessage
	minimumComparison, _ := model.CompareDesktopReleases(query.DesktopRelease, result.MinimumDesktopRelease)
	if minimumComparison < 0 {
		result.Compatibility = DesktopCompatibilityUpdateRequired
		result.Reason = "release_too_old"
		return result, nil
	}

	maximumComparison, _ := model.CompareDesktopReleases(query.DesktopRelease, result.MaximumDesktopRelease)
	if maximumComparison > 0 {
		result.Compatibility = DesktopCompatibilityServerIncompatible
		result.Reason = "release_too_new"
		return result, nil
	}

	if query.RealtimeProtocol < result.MinimumRealtimeProtocol {
		result.Compatibility = DesktopCompatibilityUpdateRequired
		result.Reason = "realtime_protocol_too_old"
		return result, nil
	}

	if query.RealtimeProtocol > result.MaximumRealtimeProtocol {
		result.Compatibility = DesktopCompatibilityServerIncompatible
		result.Reason = "realtime_protocol_too_new"
		return result, nil
	}

	if _, revoked := slices.BinarySearch(policy.RevokedBuildIDs(), query.DesktopBuildID); revoked {
		result.Compatibility = DesktopCompatibilityUpdateRequired
		result.Reason = "build_revoked"
		return result, nil
	}

	for _, entry := range matchingTarget {
		isExactBuild := entry.DesktopBuildID == query.DesktopBuildID &&
			entry.DesktopRelease == query.DesktopRelease &&
			entry.RealtimeProtocol == query.RealtimeProtocol
		if isExactBuild {
			result.Compatibility = DesktopCompatibilityCompatible
			result.Reason = "compatible"
			return result, nil
		}
	}

	result.Compatibility = DesktopCompatibilityServerIncompatible
	result.Reason = "build_unrecognized"
	return result, nil
}

func validateDesktopCompatibilityQuery(query DesktopCompatibilityQuery) error {
	selectorsValid := model.IsValidDesktopRelease(query.DesktopRelease) &&
		model.IsValidDesktopBuildID(query.DesktopBuildID) &&
		validDesktopTargetSelector.MatchString(query.Platform) &&
		validDesktopTargetSelector.MatchString(query.Architecture) &&
		query.RealtimeProtocol > 0
	if !selectorsValid {
		return apperror.New("request.invalid")
	}
	return nil
}

func desktopCompatibilityBounds(
	policy *model.DesktopCompatibilityPolicy,
	entries []model.DesktopBuildTuple,
) DesktopCompatibilityResult {
	minimumRelease := entries[0].DesktopRelease
	maximumRelease := entries[0].DesktopRelease
	minimumRealtime := entries[0].RealtimeProtocol
	maximumRealtime := entries[0].RealtimeProtocol
	for _, entry := range entries[1:] {
		if comparison, _ := model.CompareDesktopReleases(entry.DesktopRelease, minimumRelease); comparison < 0 {
			minimumRelease = entry.DesktopRelease
		}
		if comparison, _ := model.CompareDesktopReleases(entry.DesktopRelease, maximumRelease); comparison > 0 {
			maximumRelease = entry.DesktopRelease
		}
		minimumRealtime = min(minimumRealtime, entry.RealtimeProtocol)
		maximumRealtime = max(maximumRealtime, entry.RealtimeProtocol)
	}
	if policy.MinimumDesktopRelease != "" {
		if comparison, _ := model.CompareDesktopReleases(policy.MinimumDesktopRelease, minimumRelease); comparison > 0 {
			minimumRelease = policy.MinimumDesktopRelease
		}
	}
	return DesktopCompatibilityResult{
		Availability:            policy.Availability,
		RetryAt:                 policy.RetryAt,
		MinimumDesktopRelease:   minimumRelease,
		MaximumDesktopRelease:   maximumRelease,
		MinimumRealtimeProtocol: minimumRealtime,
		MaximumRealtimeProtocol: maximumRealtime,
	}
}

// ResolveNativeDeliveryBuild finds the build a retained stream was recorded
// with, even after its key authenticates a newer desktop app. It grants no
// live admission; the caller and the store check ownership.
func (s *Service) ResolveNativeDeliveryBuild(
	_ context.Context,
	identity attempt.NativeDeliveryBuildIdentity,
) (model.DesktopBuildTuple, error) {
	for _, entry := range s.desktopBuilds.entries {
		agreement := entry.NativeAgreement
		if agreement == nil {
			continue
		}

		matrix := agreement.Matrix()
		if matrix.ReleaseID == identity.ReleaseID &&
			matrix.MatrixID == identity.MatrixID &&
			matrix.TargetTuple == identity.TargetTuple &&
			agreement.MatrixDigest() == identity.MatrixDigest {
			return entry, nil
		}
	}

	return model.DesktopBuildTuple{}, errors.New("original native delivery build is missing from the verified catalog")
}
