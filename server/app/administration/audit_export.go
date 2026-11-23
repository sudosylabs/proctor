// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"context"
	"errors"
	"io"
	"slices"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/internal/exportfile"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// AuditExportContent stores audit export files, keyed by export and the Job
// attempt that wrote them. A failed write whose backend outcome is unknown
// returns an error implementing ExportWriteUncertain(), and its artifact
// stays reserved for cleanup.
type AuditExportContent interface {
	BuildAuditExport(context.Context, model.AuditExportID, model.JobAttemptID, func(io.Writer) error) (*AuditExportFile, error)
	OpenAuditExport(context.Context, model.AuditExportID, model.JobAttemptID) (io.ReadCloser, error)
	PurgeAuditExport(context.Context, model.AuditExportID, model.JobAttemptID) error
}

// AuditExportFile describes the verified file AuditExportContent stored.
type AuditExportFile struct {
	SizeBytes int64
	SHA256    string
}

// CreateAuditExportCommand asks for a file of the events the audit listing
// would show the caller for Query. Query's page bounds are ignored.
type CreateAuditExportCommand struct {
	Query          ListAuditEventsQuery
	Format         model.AuditExportFormat
	IdempotencyKey string
}

// AuditExportDownload pairs a ready export with its file, which the caller
// must close.
type AuditExportDownload struct {
	Export *model.AuditExport
	Body   io.ReadCloser
}

// auditExportPage is how many events a build reads at a time.
const auditExportPage = 200

// CreateAuditExport queues a file of the audit listing as the caller sees it
// now. The filters and the caller's audit visibility are frozen: an academic
// auditor's file carries the same redacted projection their listing does,
// and events written after the request are not in it. The request is itself
// audited.
func (s *Service) CreateAuditExport(ctx context.Context, inv invocation.Invocation, command CreateAuditExportCommand) (*model.AuditExport, error) {
	if s.auditExports == nil || s.auditExportContent == nil {
		return nil, apperror.New("audit.export.unavailable")
	}
	if !command.Format.IsValid() {
		return nil, apperror.New("audit.query.invalid")
	}
	options, err := auditListFilters(command.Query)
	if err != nil {
		return nil, err
	}
	at := model.TimeUTC(s.now())
	if options.Until.IsZero() || options.Until.After(at) {
		options.Until = at
	}
	if !options.Since.IsZero() && !options.Since.Before(options.Until) {
		return nil, apperror.New("audit.query.invalid")
	}

	prepared, err := idempotency.Require(inv, store.AuditExportCreateOperation, command.IdempotencyKey, auditExportFingerprint(command))
	if err != nil {
		return nil, err
	}
	visibility, err := s.authorizeAuditList(ctx, inv)
	if err != nil {
		return nil, err
	}
	if !validAuditVisibility(visibility) {
		return nil, access.DeniedError()
	}
	options.Visibility = visibility

	replayed, err := s.auditExports.FindCreation(ctx, prepared)
	if err != nil {
		return nil, auditExportStoreErrors.Translate(err)
	}
	if !replayed.IsZero() {
		read, err := s.loadAuditExport(ctx, inv.Principal().UserID, replayed, visibility)
		if err != nil {
			return nil, err
		}
		return read.Export, nil
	}

	var topologyRevision int64
	if visibility.UsesAcademicTopology() {
		topologyRevision, err = s.auditExports.TopologyRevision(ctx)
		if err != nil || topologyRevision < 1 {
			return nil, apperror.New("audit.export.unavailable").Wrap(err)
		}
	}
	count, err := s.audits.Count(ctx, options, model.AuditExportMaximumEvents+1)
	if err != nil {
		return nil, auditListingError(err)
	}
	if count > model.AuditExportMaximumEvents {
		return nil, apperror.New("audit.export.limit")
	}

	institution, err := s.institutions.GetSingleton(ctx)
	if err != nil {
		return nil, auditListingError(err)
	}
	action, resource := model.ActionAuditView, model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()}
	scopeType, scopeID := model.RoleScopeInstitution, institution.ID.String()
	if !visibility.InstitutionWide {
		action = model.ActionAcademicAuditView
		if len(visibility.AcademicUnitRootIDs) > 0 {
			resource = model.Resource{Type: model.ResourceAcademicUnit, ID: visibility.AcademicUnitRootIDs[0].String()}
			scopeType, scopeID = model.RoleScopeAcademicUnit, resource.ID
		}
	}

	export := &model.AuditExport{
		ID:              model.NewAuditExportID(),
		RequesterUserID: inv.Principal().UserID,
		Format:          command.Format,
		Redacted:        !visibility.InstitutionWide,
		JobID:           model.NewJobID(),
		EventCount:      count,
	}
	created, err := access.RunAudited(ctx, s.auditor,
		access.Mutation{
			Invocation: inv, Action: action, Resource: resource, ScopeType: scopeType, ScopeID: scopeID,
			Operation: "create_audit_export",
			Value:     map[string]any{"audit_export_id": export.ID.String(), "format": string(export.Format), "event_count": count},
			At:        at,
		},
		func(ctx context.Context, reference access.MutationReference) (*model.AuditExport, error) {
			return s.auditExports.Create(ctx, &store.AuditExportCreation{
				Export: export, Query: options, AuditEventID: reference.ID, AuditAt: reference.MutationAt,
				TopologyRevision: topologyRevision,
			}, prepared)
		},
		auditExportStoreErrors.Translate,
	)
	if err != nil {
		return nil, err
	}
	if created.Validate() != nil || created.RequesterUserID != inv.Principal().UserID {
		return nil, apperror.New("audit.export.unavailable").Wrap(errors.New("invalid audit export creation result"))
	}
	if created.ID != export.ID {
		read, err := s.loadAuditExport(ctx, inv.Principal().UserID, created.ID, visibility)
		if err != nil {
			return nil, err
		}
		return read.Export, nil
	}
	return created, nil
}

// GetAuditExport returns the caller's own export while they still hold the
// audit visibility it was made with.
func (s *Service) GetAuditExport(ctx context.Context, inv invocation.Invocation, id model.AuditExportID) (*model.AuditExport, error) {
	read, err := s.readAuditExport(ctx, inv, id)
	if err != nil {
		return nil, err
	}
	return read.Export, nil
}

// OpenAuditExport opens the file of the caller's own ready export.
func (s *Service) OpenAuditExport(ctx context.Context, inv invocation.Invocation, id model.AuditExportID) (*AuditExportDownload, error) {
	export, body, err := exportfile.OpenStable(ctx,
		func(ctx context.Context) (*model.AuditExport, store.AuditExportArtifact, error) {
			read, err := s.readAuditExport(ctx, inv, id)
			if err != nil {
				return nil, store.AuditExportArtifact{}, err
			}
			switch read.Export.State {
			case model.ExamExportReady:
			case model.ExamExportExpired:
				return nil, store.AuditExportArtifact{}, apperror.New("audit.export.expired")
			case model.ExamExportQueued:
				return nil, store.AuditExportArtifact{}, apperror.New("audit.export.not_ready")
			default:
				return nil, store.AuditExportArtifact{}, apperror.New("audit.export.unavailable")
			}
			if read.Artifact.ExportID != read.Export.ID || !read.Artifact.AttemptID.IsValid() {
				return nil, store.AuditExportArtifact{}, apperror.New("audit.export.unavailable").Wrap(errors.New("invalid audit export artifact"))
			}
			return read.Export, read.Artifact, nil
		},
		func(ctx context.Context, a store.AuditExportArtifact) (io.ReadCloser, error) {
			return s.auditExportContent.OpenAuditExport(ctx, a.ExportID, a.AttemptID)
		},
		func(err error) error { return apperror.New("audit.export.unavailable").Wrap(err) },
	)
	if err != nil {
		return nil, err
	}
	return &AuditExportDownload{Export: export, Body: body}, nil
}

// readAuditExport records the caller's audit authorization decision, then
// hides another user's export and refuses one whose visibility the caller no
// longer holds.
func (s *Service) readAuditExport(ctx context.Context, inv invocation.Invocation, id model.AuditExportID) (*store.AuditExportRead, error) {
	if s.auditExports == nil || s.auditExportContent == nil {
		return nil, apperror.New("audit.export.unavailable")
	}
	if !id.IsValid() {
		return nil, apperror.New("audit.export.not_found")
	}
	current, err := s.authorizeAuditList(ctx, inv)
	if err != nil {
		return nil, err
	}
	return s.loadAuditExport(ctx, inv.Principal().UserID, id, current)
}

func (s *Service) loadAuditExport(ctx context.Context, requester model.UserID, id model.AuditExportID, current store.AuditVisibilityScope) (*store.AuditExportRead, error) {
	read, err := s.auditExports.Get(ctx, id)
	if err != nil {
		return nil, auditExportStoreErrors.Translate(err)
	}
	if read == nil || read.Export.Validate() != nil || read.Export.ID != id {
		return nil, apperror.New("audit.export.unavailable").Wrap(errors.New("invalid audit export"))
	}
	if read.Export.RequesterUserID != requester {
		return nil, apperror.New("audit.export.not_found")
	}
	if read.TopologyChanged || !auditVisibilityCovers(current, read.Visibility) {
		return nil, access.DeniedError()
	}
	return read, nil
}

// BuildAuditExportFromJob writes and publishes the file of one queued export
// with the filters and visibility frozen at its request. A ready export is a
// no-op. system must match the Job execution that claimed the build.
func (s *Service) BuildAuditExportFromJob(ctx context.Context, system invocation.System, input store.AuditExportBuild) (err error) {
	if s.auditExports == nil || s.auditExportContent == nil {
		return apperror.New("audit.export.unavailable")
	}
	if !system.Valid() || system.Task != "" || system.JobID != input.JobID || system.AttemptID != input.AttemptID {
		return apperror.New("audit.export.unavailable").Wrap(errors.New("audit export build Job identity is invalid"))
	}

	snapshot, err := s.auditExports.BeginBuild(ctx, &input)
	if err != nil {
		return auditExportStoreErrors.Translate(err)
	}
	if snapshot == nil || snapshot.Export.Validate() != nil || snapshot.Export.ID != input.ExportID {
		return apperror.New("audit.export.unavailable").Wrap(errors.New("invalid audit export build snapshot"))
	}
	if snapshot.Export.State == model.ExamExportReady {
		return nil
	}

	defer func() {
		finishErr := exportfile.FinishWriter(ctx, err, auditExportWriteUncertain, func(cleanup context.Context) error {
			return s.auditExports.FinishWriter(cleanup, input.AuditExportArtifact)
		})
		if finishErr != nil && err == nil {
			err = apperror.New("audit.export.unavailable").Wrap(finishErr)
		}
	}()
	visibility := snapshot.Query.Visibility
	if snapshot.Export.State != model.ExamExportQueued ||
		!validAuditVisibility(visibility) ||
		snapshot.Export.Redacted == visibility.InstitutionWide {
		return apperror.New("audit.export.unavailable").Wrap(errors.New("invalid audit export build input"))
	}

	buildCtx, cancel := context.WithDeadline(ctx, snapshot.Export.ExpiresAt)
	defer cancel()
	events := 0
	file, err := s.auditExportContent.BuildAuditExport(buildCtx, input.ExportID, input.AttemptID, func(w io.Writer) error {
		encoder, encodeErr := newAuditExportEncoder(snapshot.Export.Format, w)
		if encodeErr != nil {
			return encodeErr
		}
		events, encodeErr = s.writeAuditExport(buildCtx, snapshot.Query, snapshot.Export.Redacted, encoder)
		return encodeErr
	})
	if err != nil {
		return apperror.New("audit.export.unavailable").Wrap(err)
	}
	if file == nil || !model.ValidAuditExportContent(file.SizeBytes, file.SHA256) {
		return apperror.New("audit.export.unavailable").Wrap(errors.New("invalid verified audit export content"))
	}

	if err = s.auditExports.Publish(ctx, &store.AuditExportPublication{
		AuditExportBuild: input, SizeBytes: file.SizeBytes, SHA256: file.SHA256, EventCount: events,
	}); err != nil {
		return auditExportStoreErrors.Translate(err)
	}
	return nil
}

// writeAuditExport reads the frozen listing page by page and encodes each
// event exactly as the listing projects it for that visibility.
func (s *Service) writeAuditExport(ctx context.Context, options store.AuditListOptions, redacted bool, encoder auditExportEncoder) (int, error) {
	options.Limit = auditExportPage + 1
	options.BeforeCreatedAt, options.BeforeID = time.Time{}, ""
	written := 0
	for {
		events, err := s.audits.List(ctx, options)
		if err != nil {
			return written, err
		}
		more := len(events) > auditExportPage
		if more {
			events = events[:auditExportPage]
		}
		if redacted {
			events = academicAuditProjection(events)
		}
		for _, event := range events {
			if event == nil {
				return written, errors.New("missing audit event")
			}
			if written++; written > model.AuditExportMaximumEvents {
				return written, errors.New("audit export exceeds its event limit")
			}
			if err = encoder.encode(event); err != nil {
				return written, err
			}
		}
		if !more || len(events) == 0 {
			return written, encoder.close()
		}
		last := events[len(events)-1]
		options.BeforeCreatedAt, options.BeforeID = last.CreatedAt, last.ID
	}
}

func auditExportFingerprint(command CreateAuditExportCommand) any {
	q := command.Query
	return struct {
		ActorID       model.UserID      `json:"actor_id"`
		SubjectUserID model.UserID      `json:"subject_user_id"`
		Actions       []string          `json:"actions"`
		Kind          model.AuditKind   `json:"kind"`
		Status        model.AuditStatus `json:"status"`
		Resource      *model.Resource   `json:"resource"`
		Since         string            `json:"since"`
		Until         string            `json:"until"`
		Ascending     bool              `json:"ascending"`
		Format        string            `json:"format"`
	}{q.ActorID, q.SubjectUserID, q.Actions, q.Kind, q.Status, q.Resource,
		formatOptionalTime(q.Since), formatOptionalTime(q.Until), q.Ascending, string(command.Format)}
}

func formatOptionalTime(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return model.FormatRFC3339(at)
}

func validAuditVisibility(v store.AuditVisibilityScope) bool {
	if v.InstitutionWide {
		return true
	}
	return len(v.AllowedActions) > 0 &&
		((v.AcademicInstitutionWide && len(v.AcademicUnitRootIDs) == 0) ||
			(!v.AcademicInstitutionWide && len(v.AcademicUnitRootIDs) > 0))
}

// auditVisibilityCovers reports whether current sees at least what frozen
// saw. A frozen subtree must still be granted as such; a grant on its
// ancestor is not followed and fails closed.
func auditVisibilityCovers(current, frozen store.AuditVisibilityScope) bool {
	switch {
	case !validAuditVisibility(current) || !validAuditVisibility(frozen):
		return false
	case current.InstitutionWide:
		return true
	case frozen.InstitutionWide:
		return false
	}
	for _, action := range frozen.AllowedActions {
		if !slices.Contains(current.AllowedActions, action) {
			return false
		}
	}
	if current.AcademicInstitutionWide {
		return true
	}
	if frozen.AcademicInstitutionWide {
		return false
	}
	for _, root := range frozen.AcademicUnitRootIDs {
		if !slices.Contains(current.AcademicUnitRootIDs, root) {
			return false
		}
	}
	return true
}

func auditExportWriteUncertain(err error) bool {
	var uncertain interface{ ExportWriteUncertain() }
	return errors.As(err, &uncertain)
}

var auditExportStoreErrors = apperror.StoreTable{
	Rules: apperror.Conflicts("audit_export", map[string]apperror.Code{
		"policy_unconfigured": "audit.export.policy_unconfigured",
		"topology_changed":    "authorization.denied",
		"expired":             "audit.export.expired",
	}),
	NotFound:    "audit.export.not_found",
	Conflict:    "audit.export.unavailable",
	Unavailable: "audit.export.unavailable",
}
