// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package academic

import (
	"context"
	"strings"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ListAcademicUnitsQuery lists direct children of a parent unit. An empty
// ParentID lists root units of the installation institution.
type ListAcademicUnitsQuery struct {
	ParentID model.AcademicUnitID
	paging.NameQuery
}

// SearchAcademicUnitsQuery searches academic units by name within the
// installation institution.
type SearchAcademicUnitsQuery struct {
	Query string
	paging.NameQuery
}

// GetAcademicUnit needs academic_unit.view on the unit.
func (s *Service) GetAcademicUnit(
	ctx context.Context,
	inv invocation.Invocation,
	id model.AcademicUnitID,
) (*AcademicUnitView, error) {
	if !id.IsValid() {
		return nil, apperror.New("request.invalid").WithField("field", "academic_unit_id")
	}
	resource := model.Resource{Type: model.ResourceAcademicUnit, ID: id.String()}
	if err := s.authorizer.Authorize(
		ctx,
		inv,
		model.ActionAcademicUnitView,
		resource,
	); err != nil {
		return nil, err
	}
	unit, err := s.academicUnits.Get(ctx, id)
	if err != nil {
		return nil, academicUnitStoreErrors.Translate(err)
	}
	summaries, err := s.summarizeAcademicUnits(ctx, inv, []*model.AcademicUnit{unit})
	if err != nil {
		return nil, err
	}
	return &AcademicUnitView{Unit: unit, AcademicUnitSummaries: summaries}, nil
}

// ListAcademicUnits lists the direct children of ParentID, or the root units
// when it is empty.
func (s *Service) ListAcademicUnits(
	ctx context.Context,
	inv invocation.Invocation,
	query ListAcademicUnitsQuery,
) (AcademicUnitPage, error) {
	parentID := query.ParentID
	if parentID != "" && !parentID.IsValid() {
		return AcademicUnitPage{}, apperror.New("request.invalid").WithField("field", "parent_id")
	}

	limit, probe, err := paging.ResolveLimit(query.Limit, "limit")
	if err != nil {
		return AcademicUnitPage{}, err
	}

	if parentID != "" {
		resource := model.Resource{Type: model.ResourceAcademicUnit, ID: parentID.String()}
		if err = s.authorizer.Authorize(
			ctx,
			inv,
			model.ActionAcademicUnitView,
			resource,
		); err != nil {
			return AcademicUnitPage{}, err
		}
	}

	var institution model.Resource
	if parentID == "" {
		institution, err = s.authorizer.AuthorizeInstitution(
			ctx,
			inv,
			model.ActionAcademicUnitView,
		)
	} else {
		institution, err = s.authorizer.InstitutionResource(ctx)
	}

	if err != nil {
		return AcademicUnitPage{}, err
	}

	units, err := s.academicUnits.ListChildren(ctx, model.InstitutionID(institution.ID), parentID, store.AcademicNameListOptions{
		AfterName: query.AfterName,
		AfterID:   query.AfterID,
		Limit:     probe,
	})
	if err != nil {
		return AcademicUnitPage{}, academicUnitStoreErrors.Translate(err)
	}

	return s.academicUnitPage(ctx, inv, paging.New(units, limit))
}

// SearchAcademicUnits needs institution-wide academic_unit.view.
func (s *Service) SearchAcademicUnits(
	ctx context.Context,
	inv invocation.Invocation,
	query SearchAcademicUnitsQuery,
) (AcademicUnitPage, error) {
	limit, probe, err := paging.ResolveLimit(query.Limit, "limit")
	if err != nil {
		return AcademicUnitPage{}, err
	}

	institution, err := s.authorizer.AuthorizeInstitution(
		ctx,
		inv,
		model.ActionAcademicUnitView,
	)
	if err != nil {
		return AcademicUnitPage{}, err
	}

	units, err := s.academicUnits.Search(ctx, model.InstitutionID(institution.ID), store.AcademicNameListOptions{
		Term:      strings.TrimSpace(query.Query),
		AfterName: query.AfterName,
		AfterID:   query.AfterID,
		Limit:     probe,
	})
	if err != nil {
		return AcademicUnitPage{}, academicUnitStoreErrors.Translate(err)
	}

	return s.academicUnitPage(ctx, inv, paging.New(units, limit))
}

func (s *Service) academicUnitPage(
	ctx context.Context,
	inv invocation.Invocation,
	page paging.Page[*model.AcademicUnit],
) (AcademicUnitPage, error) {
	summaries, err := s.summarizeAcademicUnits(ctx, inv, page.Items)
	if err != nil {
		return AcademicUnitPage{}, err
	}
	return AcademicUnitPage{Page: page, AcademicUnitSummaries: summaries}, nil
}

var academicUnitStoreErrors = apperror.AdministrationStoreTable("academic_unit", "academic_unit.conflict", "academic_unit.invalid", nil)
