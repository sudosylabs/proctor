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

// GetAcademicUnitQuery identifies one academic unit to retrieve.
type GetAcademicUnitQuery struct {
	ID model.AcademicUnitID
}

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

// GetAcademicUnit returns one academic unit after authorizing
// academic_unit.view on that unit.
func (s *Service) GetAcademicUnit(
	ctx context.Context,
	invocation invocation.Invocation,
	query GetAcademicUnitQuery,
) (*model.AcademicUnit, error) {
	id := query.ID
	if !id.IsValid() {
		return nil, apperror.New("request.invalid").WithField("field", "academic_unit_id")
	}
	resource := model.Resource{Type: model.ResourceAcademicUnit, ID: id.String()}
	if err := s.authorizer.Authorize(
		ctx,
		invocation,
		model.ActionAcademicUnitView,
		resource,
	); err != nil {
		return nil, err
	}
	unit, err := s.academicUnits.Get(ctx, id)
	if err != nil {
		return nil, academicUnitStoreErrors.Translate(err)
	}
	return unit, nil
}

// ListAcademicUnits lists direct children under ParentID, or root units when
// ParentID is empty.
func (s *Service) ListAcademicUnits(
	ctx context.Context,
	invocation invocation.Invocation,
	query ListAcademicUnitsQuery,
) (paging.Page[*model.AcademicUnit], error) {
	parentID := query.ParentID
	if parentID != "" && !parentID.IsValid() {
		return paging.Page[*model.AcademicUnit]{}, apperror.New("request.invalid").WithField("field", "parent_id")
	}

	limit, probe, err := paging.ResolveLimit(query.Limit, "limit")
	if err != nil {
		return paging.Page[*model.AcademicUnit]{}, err
	}

	if parentID != "" {
		resource := model.Resource{Type: model.ResourceAcademicUnit, ID: parentID.String()}
		if err = s.authorizer.Authorize(
			ctx,
			invocation,
			model.ActionAcademicUnitView,
			resource,
		); err != nil {
			return paging.Page[*model.AcademicUnit]{}, err
		}
	}

	var institution model.Resource
	if parentID == "" {
		institution, err = s.authorizer.AuthorizeInstitution(
			ctx,
			invocation,
			model.ActionAcademicUnitView,
		)
	} else {
		institution, err = s.authorizer.InstitutionResource(ctx)
	}

	if err != nil {
		return paging.Page[*model.AcademicUnit]{}, err
	}

	units, err := s.academicUnits.ListChildren(ctx, model.InstitutionID(institution.ID), parentID, store.AcademicNameListOptions{
		AfterName: query.AfterName,
		AfterID:   query.AfterID,
		Limit:     probe,
	})
	if err != nil {
		return paging.Page[*model.AcademicUnit]{}, academicUnitStoreErrors.Translate(err)
	}

	return paging.New(units, limit), nil
}

// SearchAcademicUnits searches units by name through institution-scoped
// academic_unit.view.
func (s *Service) SearchAcademicUnits(
	ctx context.Context,
	invocation invocation.Invocation,
	query SearchAcademicUnitsQuery,
) (paging.Page[*model.AcademicUnit], error) {
	limit, probe, err := paging.ResolveLimit(query.Limit, "limit")
	if err != nil {
		return paging.Page[*model.AcademicUnit]{}, err
	}

	institution, err := s.authorizer.AuthorizeInstitution(
		ctx,
		invocation,
		model.ActionAcademicUnitView,
	)
	if err != nil {
		return paging.Page[*model.AcademicUnit]{}, err
	}

	units, err := s.academicUnits.Search(ctx, model.InstitutionID(institution.ID), store.AcademicNameListOptions{
		Term:      strings.TrimSpace(query.Query),
		AfterName: query.AfterName,
		AfterID:   query.AfterID,
		Limit:     probe,
	})
	if err != nil {
		return paging.Page[*model.AcademicUnit]{}, academicUnitStoreErrors.Translate(err)
	}

	return paging.New(units, limit), nil
}

var academicUnitStoreErrors = apperror.AdministrationStoreTable("academic_unit", "academic_unit.conflict", "academic_unit.invalid", nil)
