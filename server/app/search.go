// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// SearchType names one kind of result a search can return.
type SearchType string

const (
	// SearchTypeExam matches Exams by their current Draft title.
	SearchTypeExam SearchType = "exam"
	// SearchTypeClass matches Classes by name or display name.
	SearchTypeClass SearchType = "class"
	// SearchTypeUser matches Users by their visible directory fields.
	SearchTypeUser SearchType = "user"
)

const (
	searchQueryMinimumLength = 2
	searchQueryMaximumLength = 100
	searchDefaultLimit       = 5
	searchMaximumLimit       = 20
)

// SearchQuery is one cross-type search. Query is trimmed and must hold 2 to
// 100 characters. Types defaults to every type and may not repeat a type.
// Limit bounds each type separately: 1 to 20, default 5.
type SearchQuery struct {
	Query string
	Types []SearchType
	Limit int
}

// SearchResult holds the matches of each requested type, each within the
// caller's own list visibility for that type. A type that was not requested,
// or that the caller has no authority to list, has no matches.
type SearchResult struct {
	Exams   []store.ExamSummary
	Classes []*model.Class
	Users   []*model.User
}

type searchExamCatalog interface {
	ListExams(context.Context, invocation.Invocation, exam.ListQuery) (exam.CatalogPage, error)
}

type searchUserDirectory interface {
	searchUserPage(context.Context, invocation.Invocation, SearchUsersQuery) (paging.Page[*model.User], error)
}

type searchClassAuthorizer interface {
	AuthorizeClassSearch(context.Context, invocation.Invocation) (store.ClassVisibilityScope, error)
}

type searchClassStore interface {
	SearchVisible(context.Context, store.ClassSearchOptions) ([]*model.Class, error)
}

// searchServiceDependencies are the collaborators of searchService. Exams and
// Users are the Exam catalog and User directory use cases themselves, so each
// type is authorized, audited and scoped exactly as its own list is.
type searchServiceDependencies struct {
	Exams              searchExamCatalog
	Users              searchUserDirectory
	ClassAuthorization searchClassAuthorizer
	Classes            searchClassStore
}

type searchService struct {
	deps searchServiceDependencies
}

func newSearchService(deps searchServiceDependencies) *searchService {
	return &searchService{deps: deps}
}

// Search finds Exams by Draft title, Classes by name or display name, and
// Users by directory fields. Each requested type runs its own list
// authorization; a type the caller may not list contributes no matches, and
// the search is denied only when the caller may list none of the requested
// types. The query text never enters logs or audit records.
func (s *searchService) Search(ctx context.Context, invocation invocation.Invocation, query SearchQuery) (SearchResult, error) {
	term, types, limit, err := validateSearchQuery(query)
	if err != nil {
		return SearchResult{}, err
	}
	result := SearchResult{Exams: []store.ExamSummary{}, Classes: []*model.Class{}, Users: []*model.User{}}
	denied := 0
	for _, kind := range types {
		var searchErr error
		switch kind {
		case SearchTypeExam:
			var page exam.CatalogPage
			page, searchErr = s.deps.Exams.ListExams(ctx, invocation, exam.ListQuery{Query: term, Limit: limit})
			if searchErr == nil {
				result.Exams = page.Items
			}
		case SearchTypeClass:
			result.Classes, searchErr = s.searchClasses(ctx, invocation, term, limit)
		case SearchTypeUser:
			var page paging.Page[*model.User]
			page, searchErr = s.deps.Users.searchUserPage(ctx, invocation, SearchUsersQuery{Query: term, Limit: limit})
			if searchErr == nil {
				result.Users = page.Items
			}
		}
		if apperror.Is(searchErr, "authorization.denied") {
			denied++
			continue
		}
		if searchErr != nil {
			return SearchResult{}, searchErr
		}
	}
	if denied == len(types) {
		return SearchResult{}, access.DeniedError()
	}
	return result, nil
}

func (s *searchService) searchClasses(ctx context.Context, invocation invocation.Invocation, term string, limit int) ([]*model.Class, error) {
	visibility, err := s.deps.ClassAuthorization.AuthorizeClassSearch(ctx, invocation)
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

func validateSearchQuery(query SearchQuery) (string, []SearchType, int, error) {
	term := strings.TrimSpace(query.Query)
	if length := utf8.RuneCountInString(term); length < searchQueryMinimumLength || length > searchQueryMaximumLength {
		return "", nil, 0, apperror.Invalid("search.query.invalid", "q")
	}
	limit := query.Limit
	if limit == 0 {
		limit = searchDefaultLimit
	}
	if limit < 1 || limit > searchMaximumLimit {
		return "", nil, 0, apperror.Invalid("search.query.invalid", "limit")
	}
	types := query.Types
	if len(types) == 0 {
		types = []SearchType{SearchTypeExam, SearchTypeClass, SearchTypeUser}
	}
	seen := make(map[SearchType]struct{}, len(types))
	for _, kind := range types {
		_, repeated := seen[kind]
		switch {
		case repeated:
			return "", nil, 0, apperror.Invalid("search.query.invalid", "types")
		case kind == SearchTypeExam, kind == SearchTypeClass, kind == SearchTypeUser:
			seen[kind] = struct{}{}
		default:
			return "", nil, 0, apperror.Invalid("search.query.invalid", "types")
		}
	}
	return term, types, limit, nil
}
