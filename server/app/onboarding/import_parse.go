// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type onboardingImportJobCommand struct {
	ImportID string `json:"import_id"`
}

func (s *Service) parseImport(ctx context.Context, id model.OnboardingImportID) error {
	current, err := s.imports.GetOnboardingImport(ctx, id)
	if err != nil {
		return err
	}
	if current.State == model.OnboardingImportPreviewReady {
		return s.content.RemoveOnboardingImport(context.WithoutCancel(ctx), id)
	}
	if current.State == model.OnboardingImportFailed || current.State == model.OnboardingImportCanceled {
		return s.content.RemoveOnboardingImport(context.WithoutCancel(ctx), id)
	}
	if current.State != model.OnboardingImportParsing {
		return store.NewErrConflict("onboarding_import", "state", nil)
	}

	actorInvocation := invocation.New(current.Principal, model.RequestMetadata{})
	if err = s.validatePrincipal(ctx, current.Principal); err != nil {
		if onboardingImportRetryableError(err) {
			return err
		}
		if _, failErr := s.failImport(ctx, id, "authentication.token_invalid"); failErr != nil {
			return failErr
		}
		if removeErr := s.content.RemoveOnboardingImport(context.WithoutCancel(ctx), id); removeErr != nil {
			return removeErr
		}

		return err
	}

	if err = s.authorize(
		ctx,
		actorInvocation,
		model.ActionOnboardingBatchManage,
		onboardingImportResource(current.ScopeType, current.ScopeID),
	); err != nil {
		if onboardingImportRetryableError(err) {
			return err
		}
		if _, failErr := s.failImport(ctx, id, invitationBatchPublicErrorCode(err)); failErr != nil {
			return failErr
		}
		if removeErr := s.content.RemoveOnboardingImport(context.WithoutCancel(ctx), id); removeErr != nil {
			return removeErr
		}

		return err
	}

	if current.Mode != model.OnboardingImportInstitution {
		if err = s.validateExternalTarget(ctx, actorInvocation, current.Mode, current.ScopeID, current.RoleID.String(), true); err != nil {
			if onboardingImportRetryableError(err) {
				return err
			}
			if _, failErr := s.failImport(ctx, id, invitationBatchPublicErrorCode(err)); failErr != nil {
				return failErr
			}
			if removeErr := s.content.RemoveOnboardingImport(context.WithoutCancel(ctx), id); removeErr != nil {
				return removeErr
			}

			return err
		}
	}

	reader, err := s.content.OpenOnboardingImport(ctx, id)
	if err != nil {
		return err
	}

	defer func() { _ = reader.Close() }()
	rows, ignored, digest, err := s.parseImportCSV(ctx, actorInvocation, current, reader)
	if err != nil {
		if !errors.Is(err, errOnboardingImportInvalidFile) {
			return err
		}
		if _, failErr := s.failImport(ctx, id, "onboarding_import.file_invalid"); failErr != nil {
			return failErr
		}
		if removeErr := s.content.RemoveOnboardingImport(context.WithoutCancel(ctx), id); removeErr != nil {
			return removeErr
		}

		return err
	}

	if err = requireOnboardingImportPreviewCodes(rows); err != nil {
		return err
	}

	_, err = s.imports.CompleteOnboardingImportPreview(ctx, &store.OnboardingImportPreviewCompletion{
		ID:               id,
		ExpectedRevision: current.Revision,
		Digest:           digest,
		IgnoredHeaders:   ignored,
		Rows:             rows,
		At:               model.TimeUTC(s.now()),
	})
	if err != nil {
		return err
	}

	return s.content.RemoveOnboardingImport(context.WithoutCancel(ctx), id)
}

var errOnboardingImportInvalidFile = errors.New("onboarding import file is invalid")

var onboardingImportKnownHeaders = map[string]struct{}{
	"email":            {},
	"reference":        {},
	"kind":             {},
	"username":         {},
	"display_name":     {},
	"first_name":       {},
	"last_name":        {},
	"locale":           {},
	"timezone":         {},
	"start_at":         {},
	"end_at":           {},
	"academic_unit":    {},
	"academic_period":  {},
	"programme":        {},
	"programme_level":  {},
	"class":            {},
	"role":             {},
	"operation":        {},
	"user_id":          {},
	"relationship_id":  {},
	"role_id":          {},
	"affiliation_kind": {},
}

var academicAdministrationImportHeaders = map[string]struct{}{
	"operation":        {},
	"reference":        {},
	"user_id":          {},
	"relationship_id":  {},
	"role_id":          {},
	"affiliation_kind": {},
	"start_at":         {},
	"end_at":           {},
}

func (s *Service) parseImportCSV(
	ctx context.Context,
	inv invocation.Invocation,
	current *store.OnboardingImport,
	body io.Reader,
) ([]store.OnboardingImportRow, []string, string, error) {
	data, err := io.ReadAll(io.LimitReader(body, store.OnboardingImportMaximumBytes+1))
	if err != nil {
		return nil, nil, "", err
	}
	if len(data) > store.OnboardingImportMaximumBytes || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, nil, "", invalidOnboardingImportFile("invalid CSV encoding or size")
	}

	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	reader := csv.NewReader(bytes.NewReader(data))
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil || len(header) < 1 || len(header) > store.OnboardingImportMaximumColumns {
		return nil, nil, "", invalidOnboardingImportFile("invalid CSV header")
	}

	indexes, ignored, err := parseOnboardingImportHeader(current.Mode, header)
	if err != nil {
		return nil, nil, "", err
	}

	rows := make([]store.OnboardingImportRow, 0)
	for number := 1; ; number++ {
		record, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil || len(record) != len(header) || number > store.OnboardingImportMaximumRows {
			return nil, nil, "", invalidOnboardingImportFile("invalid CSV row")
		}

		row, rowErr := s.parseOnboardingImportRow(ctx, inv, current, number, indexes, record)
		if rowErr != nil {
			return nil, nil, "", rowErr
		}

		rows = append(rows, row)
	}

	if len(rows) == 0 {
		return nil, nil, "", invalidOnboardingImportFile("CSV has no data rows")
	}

	markOnboardingImportDuplicates(rows)
	digest, err := onboardingImportDigest(ignored, rows)
	if err != nil {
		return nil, nil, "", err
	}

	return rows, ignored, digest, nil
}

// parseOnboardingImportHeader validates the CSV header for the import mode and returns the column indexes and the
// sorted names of ignored columns.
func parseOnboardingImportHeader(mode model.OnboardingImportMode, header []string) (map[string]int, []string, error) {
	indexes := make(map[string]int, len(header))
	ignored := make([]string, 0)
	for index, raw := range header {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" || len(name) > 128 || model.SanitizeUnicode(name) != name {
			return nil, nil, invalidOnboardingImportFile("invalid CSV header")
		}
		if _, exists := indexes[name]; exists {
			return nil, nil, invalidOnboardingImportFile("duplicate CSV header")
		}

		indexes[name] = index
		if mode == model.OnboardingImportAcademicAdministration {
			if _, globallyKnown := onboardingImportKnownHeaders[name]; globallyKnown {
				if _, allowed := academicAdministrationImportHeaders[name]; !allowed {
					return nil, nil, invalidOnboardingImportFile("header is not allowed for academic administration")
				}
			}
		}

		if _, known := onboardingImportKnownHeaders[name]; !known {
			ignored = append(ignored, name)
		}
	}

	if mode == model.OnboardingImportAcademicAdministration {
		if _, exists := indexes["operation"]; !exists {
			return nil, nil, invalidOnboardingImportFile("operation header is required")
		}
	} else if _, exists := indexes["email"]; !exists {
		return nil, nil, invalidOnboardingImportFile("email header is required")
	}

	slices.Sort(ignored)
	return indexes, ignored, nil
}

// parseOnboardingImportRow validates one CSV record, builds its import row, and resolves its preview status; the
// returned error is either an invalid-file error or a retryable resolution error.
func (s *Service) parseOnboardingImportRow(
	ctx context.Context,
	inv invocation.Invocation,
	current *store.OnboardingImport,
	number int,
	indexes map[string]int,
	record []string,
) (store.OnboardingImportRow, error) {
	values := make(map[string]string, len(indexes))
	for name, index := range indexes {
		value := strings.TrimSpace(record[index])
		if len(value) > store.OnboardingImportMaximumField || model.SanitizeUnicode(value) != value {
			return store.OnboardingImportRow{}, invalidOnboardingImportFile("invalid CSV field")
		}

		values[name] = value
	}

	row := store.OnboardingImportRow{
		ImportID:        current.ID,
		RowNumber:       number,
		Reference:       values["reference"],
		ScopeType:       current.ScopeType,
		ScopeID:         current.ScopeID,
		Email:           strings.ToLower(values["email"]),
		UserID:          model.UserID(values["user_id"]),
		RelationshipID:  values["relationship_id"],
		AffiliationKind: model.AffiliationKind(values["affiliation_kind"]),
		Username:        values["username"],
		DisplayName:     values["display_name"],
		FirstName:       values["first_name"],
		LastName:        values["last_name"],
		Locale:          values["locale"],
		Timezone:        values["timezone"],
		UpdatedAt:       model.TimeUTC(s.now()),
	}
	if values["role_id"] != "" {
		row.RoleID = model.RoleID(values["role_id"])
	}
	if row.Reference == "" {
		row.Reference = strconv.Itoa(number)
	}
	if len(row.Reference) > 128 {
		row.Reference = strconv.Itoa(number)
	}
	if len(row.Email) > model.UserEmailMaxLength {
		row.Email = ""
	}

	var boundsErr error
	row.StartsAt, row.EndsAt, boundsErr = parseOnboardingImportBounds(values["start_at"], values["end_at"])
	resolveErr := s.resolveAndValidateImportRow(ctx, inv, current, values, &row)
	if onboardingImportRetryableError(resolveErr) {
		return store.OnboardingImportRow{}, resolveErr
	}

	if boundsErr != nil {
		row.PreviewStatus, row.Status, row.PreviewCode = model.OnboardingImportRowInvalid, model.OnboardingImportRowInvalid, "request.invalid"
	} else if resolveErr != nil {
		row.PreviewStatus, row.Status, row.PreviewCode =
			model.OnboardingImportRowInvalid,
			model.OnboardingImportRowInvalid,
			onboardingImportPreviewErrorCode(resolveErr)
	} else {
		row.PreviewStatus, row.Status = model.OnboardingImportRowValid, model.OnboardingImportRowValid
	}

	return row, nil
}

// markOnboardingImportDuplicates marks every row whose duplicate key occurs more than once as a duplicate.
func markOnboardingImportDuplicates(rows []store.OnboardingImportRow) {
	duplicates := make(map[string]int, len(rows))
	for _, row := range rows {
		if key := onboardingImportDuplicateKey(row); key != "" {
			duplicates[key]++
		}
	}

	for index := range rows {
		if key := onboardingImportDuplicateKey(rows[index]); key != "" && duplicates[key] > 1 {
			rows[index].PreviewStatus, rows[index].Status, rows[index].PreviewCode =
				model.OnboardingImportRowDuplicate,
				model.OnboardingImportRowDuplicate,
				"onboarding_batch.duplicate"
		}
	}
}

// onboardingImportDigest computes the hex SHA-256 digest of the ignored headers and the import-independent row
// content.
func onboardingImportDigest(ignored []string, rows []store.OnboardingImportRow) (string, error) {
	digestRows := make([]store.OnboardingImportRow, len(rows))
	copy(digestRows, rows)
	for index := range digestRows {
		digestRows[index].ImportID = ""
		digestRows[index].UpdatedAt = time.Time{}
	}
	document, err := json.Marshal(struct {
		Version int                         `json:"version"`
		Ignored []string                    `json:"ignored"`
		Rows    []store.OnboardingImportRow `json:"rows"`
	}{1, ignored, digestRows})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(document)
	return hex.EncodeToString(digest[:]), nil
}

func invalidOnboardingImportFile(message string) error {
	return fmt.Errorf("%w: %s", errOnboardingImportInvalidFile, message)
}

func parseOnboardingImportBounds(start, end string) (model.OptionalTime, model.OptionalTime, error) {
	parse := func(value string) (model.OptionalTime, error) {
		if value == "" {
			return model.OptionalTime{}, nil
		}
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return model.OptionalTime{}, err
		}
		return model.OptionalTimeFrom(parsed), nil
	}
	starts, err := parse(start)
	if err != nil {
		return model.OptionalTime{}, model.OptionalTime{}, err
	}
	ends, err := parse(end)
	if err != nil || (ends.Valid && !ends.Time.After(starts.Time)) {
		return model.OptionalTime{}, model.OptionalTime{}, errors.New("invalid effective bounds")
	}
	return starts, ends, nil
}

func onboardingImportDuplicateKey(row store.OnboardingImportRow) string {
	if row.Operation == "" || !row.ScopeType.IsValid() || !model.IsValidOpaqueID(row.ScopeID) {
		return ""
	}

	if row.UserID.IsValid() || model.IsValidOpaqueID(row.RelationshipID) {
		key := row.Operation + "\x00" +
			string(row.ScopeType) + "\x00" +
			row.ScopeID + "\x00" +
			row.UserID.String() + "\x00" +
			row.RelationshipID + "\x00" +
			row.RoleID.String()
		if AcademicAdministrationBatchOperation(row.Operation).IsValid() {
			key += "\x00" + string(row.AffiliationKind) + "\x00" + row.StartsAt.FormatRFC3339() + "\x00" + row.EndsAt.FormatRFC3339()
		}

		return key
	}

	if row.Email == "" {
		return ""
	}

	return row.Operation + "\x00" +
		string(row.ScopeType) + "\x00" +
		row.ScopeID + "\x00" +
		strings.ToLower(row.Email) + "\x00" +
		row.RoleID.String()
}

func onboardingImportPreviewErrorCode(err error) string {
	if _, ok := apperror.As(err); !ok {
		return "request.invalid"
	}
	return invitationBatchPublicErrorCode(err)
}
