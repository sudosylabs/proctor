// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package search

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Type names one kind of result a search can return.
type Type string

const (
	// TypeExam matches Exams by their current Draft title.
	TypeExam Type = "exam"
	// TypeClass matches Classes by name or display name.
	TypeClass Type = "class"
	// TypeUser matches Users by their visible directory fields.
	TypeUser Type = "user"
)

const (
	queryMinimumLength = 2
	queryMaximumLength = 100
	defaultLimit       = 5
	maximumLimit       = 20
)

// Query is one cross-type search. Query is trimmed and must hold 2 to
// 100 characters. Types defaults to every type and may not repeat a type.
// Limit bounds each type separately: 1 to 20, default 5.
type Query struct {
	Query string
	Types []Type
	Limit int
}

// Result holds the matches of each requested type, each within the
// caller's own list visibility for that type. A type that was not requested,
// or that the caller has no authority to list, has no matches.
type Result struct {
	Exams   []store.ExamSummary
	Classes []*model.Class
	Users   []*model.User
}

type examCatalog interface {
	ListExams(context.Context, invocation.Invocation, exam.ListQuery) (exam.CatalogPage, error)
}

type userDirectory interface {
	SearchUserPage(context.Context, invocation.Invocation, user.SearchUsersQuery) (paging.Page[*model.User], error)
}

type classAuthorizer interface {
	AuthorizeClassSearch(context.Context, invocation.Invocation) (store.ClassVisibilityScope, error)
}

// Dependencies are the collaborators of Service. Exams and Users are the
// Exam catalog and User directory use cases themselves, so each type is
// authorized, audited and scoped exactly as its own list is.
type Dependencies struct {
	Exams              examCatalog
	Users              userDirectory
	ClassAuthorization classAuthorizer
	Classes            store.ClassStore
}

// Service finds Exams, Classes and Users within the caller's own list
// visibility for each type.
type Service struct {
	deps Dependencies
}

// New builds the search Service over the services that own each searched
// type.
func New(deps Dependencies) *Service {
	return &Service{deps: deps}
}

// Search finds Exams by Draft title, Classes by name or display name, and
// Users by directory fields. Each requested type runs its own list
// authorization; a type the caller may not list contributes no matches, and
// the search is denied only when the caller may list none of the requested
// types. The query text never enters logs or audit records.
func (s *Service) Search(ctx context.Context, inv invocation.Invocation, query Query) (Result, error) {
	term, types, limit, err := validateQuery(query)
	if err != nil {
		return Result{}, err
	}

	result := Result{Exams: []store.ExamSummary{}, Classes: []*model.Class{}, Users: []*model.User{}}
	denied := 0
	for _, kind := range types {
		var searchErr error
		switch kind {
		case TypeExam:
			var page exam.CatalogPage
			page, searchErr = s.deps.Exams.ListExams(ctx, inv, exam.ListQuery{Query: term, Limit: limit})
			if searchErr == nil {
				result.Exams = page.Items
			}
		case TypeClass:
			result.Classes, searchErr = s.searchClasses(ctx, inv, term, limit)
		case TypeUser:
			var page paging.Page[*model.User]
			page, searchErr = s.deps.Users.SearchUserPage(ctx, inv, user.SearchUsersQuery{Query: term, Limit: limit})
			if searchErr == nil {
				result.Users = page.Items
			}
		}

		if apperror.Is(searchErr, "authorization.denied") {
			denied++
			continue
		}

		if searchErr != nil {
			return Result{}, searchErr
		}
	}

	if denied == len(types) {
		return Result{}, access.DeniedError()
	}

	return result, nil
}

func (s *Service) searchClasses(
	ctx context.Context,
	inv invocation.Invocation,
	term string,
	limit int,
) ([]*model.Class, error) {
	visibility, err := s.deps.ClassAuthorization.AuthorizeClassSearch(ctx, inv)
	if err != nil {
		return nil, err
	}

	classes, err := s.deps.Classes.SearchVisible(ctx, store.ClassSearchOptions{Term: term, Limit: limit, Visibility: visibility})
	if err != nil {
		return nil, classSearchStoreErrors.Translate(err)
	}

	return classes, nil
}

// classSearchStoreErrors presents every Class search Store failure as
// unavailable: the search reads no single Class whose absence or conflict a
// caller could act on.
var classSearchStoreErrors = apperror.StoreTable{Unavailable: "administration.unavailable"}

func validateQuery(query Query) (string, []Type, int, error) {
	term := strings.TrimSpace(query.Query)
	if length := utf8.RuneCountInString(term); length < queryMinimumLength || length > queryMaximumLength {
		return "", nil, 0, apperror.Invalid("search.query.invalid", "q")
	}
	limit := query.Limit
	if limit == 0 {
		limit = defaultLimit
	}
	if limit < 1 || limit > maximumLimit {
		return "", nil, 0, apperror.Invalid("search.query.invalid", "limit")
	}
	types := query.Types
	if len(types) == 0 {
		types = []Type{TypeExam, TypeClass, TypeUser}
	}
	seen := make(map[Type]struct{}, len(types))
	for _, kind := range types {
		_, repeated := seen[kind]
		switch {
		case repeated:
			return "", nil, 0, apperror.Invalid("search.query.invalid", "types")
		case kind == TypeExam, kind == TypeClass, kind == TypeUser:
			seen[kind] = struct{}{}
		default:
			return "", nil, 0, apperror.Invalid("search.query.invalid", "types")
		}
	}
	return term, types, limit, nil
}
