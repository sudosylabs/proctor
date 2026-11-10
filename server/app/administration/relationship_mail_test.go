// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type relationshipUserStoreTestFake struct{}

func (relationshipUserStoreTestFake) Get(_ context.Context, id model.UserID) (*model.User, error) {
	parsed, err := model.ParseUserID(id.String())
	if err != nil {
		return nil, err
	}
	return &model.User{ID: parsed, Revision: 1}, nil
}

func (relationshipUserStoreTestFake) ListIdentities(_ context.Context, ids []model.UserID) ([]store.UserIdentity, error) {
	identities := make([]store.UserIdentity, 0, len(ids))
	for _, id := range ids {
		identities = append(identities, store.UserIdentity{ID: id, Username: "user-" + id.String(), DisplayName: "User " + id.String(), Email: id.String() + "@example.edu"})
	}
	return identities, nil
}

type relationshipMailPreparerTestFake struct {
	requests []mail.RelationshipTransitionPreparation
}

func (f *relationshipMailPreparerTestFake) PrepareRelationshipTransition(request mail.RelationshipTransitionPreparation) (*store.PreparedMail, error) {
	f.requests = append(f.requests, request)
	return &store.PreparedMail{}, nil
}
