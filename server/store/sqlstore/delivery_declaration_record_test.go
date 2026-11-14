// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestRetainedDeliveryDeclarationValidation(t *testing.T) {
	declaration := model.DeclareDeliveryGaps{DeclarationID: model.NewID(), AllocatedThroughSequence: 4, Ranges: []model.SequenceRange{{First: 1, Last: 2}}, Reason: "spool_lost"}
	raw, err := declaration.Canonical()
	require.NoError(t, err)
	receipt := model.DeliveryGapReceipt{DeclarationID: declaration.DeclarationID, RequestDigest: model.SHA256Fingerprint(raw), DeclarationRevision: 1, SettledThroughSequence: 2}
	valid := deliveryDeclarationRecord{Kind: "gaps", RequestDigest: receipt.RequestDigest, Gaps: &declaration, Receipt: receipt}
	for _, tc := range []struct {
		name   string
		change func(*deliveryDeclarationRecord)
	}{
		{"negative settlement", func(r *deliveryDeclarationRecord) { r.Receipt.SettledThroughSequence = -1 }},
		{"wrong receipt identity", func(r *deliveryDeclarationRecord) { r.Receipt.DeclarationID = model.NewID() }},
		{"wrong request digest", func(r *deliveryDeclarationRecord) { r.RequestDigest = model.SHA256Fingerprint([]byte("other")) }},
		{"wrong kind", func(r *deliveryDeclarationRecord) { r.Kind = "unknown" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := valid
			tc.change(&value)
			encoded, marshalErr := json.Marshal(value)
			require.NoError(t, marshalErr)
			_, marshalErr = decodeDeliveryDeclarationRecord(encoded)
			require.ErrorIs(t, marshalErr, store.ErrInvalidState)
		})
	}
	raw, err = json.Marshal(valid)
	require.NoError(t, err)
	_, err = decodeDeliveryDeclarationRecord(raw)
	require.NoError(t, err)
	_, capacityErr := model.ReserveDeliveryMetadata(model.DeliveryMetadataLimitBytes, 1)
	var capacity *model.DeliveryMetadataCapacity
	require.ErrorAs(t, capacityErr, &capacity)
	for _, tc := range []struct {
		name, refusal string
		receipt       model.DeliveryGapReceipt
		capacity      *model.DeliveryMetadataCapacity
		valid         bool
	}{
		{name: "receipt", receipt: receipt, valid: true},
		{name: "refusal", refusal: "sequence_limit", valid: true},
		{name: "capacity", capacity: capacity, valid: true},
		{name: "unknown refusal", refusal: "unknown"},
		{name: "missing receipt"},
		{name: "invalid capacity", capacity: &model.DeliveryMetadataCapacity{}},
		{name: "mixed refusal", refusal: "sequence_limit", receipt: receipt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDeliveryDeclarationOutcome(tc.refusal, tc.receipt, tc.capacity)
			require.False(t, tc.valid && err != nil, "valid=%v err=%v", tc.valid, err)
			require.False(t, !tc.valid && !errors.Is(err, store.ErrInvalidState), "valid=%v err=%v", tc.valid, err)
		})
	}
}

func TestRetainedSourceRefusalValidation(t *testing.T) {
	valid := browserSourceRefusalRecord{SourceSessionID: "00000000-0000-4000-8000-000000000001", RequestDigest: model.SHA256Fingerprint([]byte("source")), Code: "exam.browser.source_budget_exhausted"}
	require.NoError(t, valid.Validate())
	for _, tc := range []struct {
		name   string
		change func(*browserSourceRefusalRecord)
	}{
		{"code", func(r *browserSourceRefusalRecord) { r.Code = "unknown" }},
		{"digest", func(r *browserSourceRefusalRecord) { r.RequestDigest = "invalid" }},
		{"missing capacity", func(r *browserSourceRefusalRecord) { r.Code = "exam.delivery.metadata_capacity" }},
		{"invalid capacity", func(r *browserSourceRefusalRecord) {
			r.Code = "exam.delivery.metadata_capacity"
			r.Capacity = &model.DeliveryMetadataCapacity{}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := valid
			tc.change(&value)
			require.Error(t, value.Validate(), "invalid refusal accepted")
		})
	}
}
