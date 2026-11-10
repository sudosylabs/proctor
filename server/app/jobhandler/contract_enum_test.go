// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package jobhandler

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// contractOnboardingService only lets testDescriptors include the onboarding
// Job descriptors; the contract tests never run a handler.
type contractOnboardingService struct{}

func (contractOnboardingService) PreviewProgression(context.Context, model.OnboardingImportID) (string, error) {
	return "", nil
}

func (contractOnboardingService) Parse(context.Context, model.OnboardingImportID) (string, error) {
	return "", nil
}

func (contractOnboardingService) Get(context.Context, model.OnboardingImportID) (*store.OnboardingImport, error) {
	return nil, nil //nolint:nilnil // never called
}

func (contractOnboardingService) Execute(context.Context, model.OnboardingImportID, int, func(int, int) error) error {
	return nil
}

func (contractOnboardingService) Fail(context.Context, model.OnboardingImportID, string) error {
	return nil
}

func contractComponentEnum(t *testing.T, name string) []string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	encoded, err := os.ReadFile(filepath.Join(filepath.Dir(currentFile), "..", "..", "openapi.json"))
	require.NoError(t, err)
	var document struct {
		Components struct {
			Schemas map[string]struct {
				Enum []string `json:"enum"`
			} `json:"schemas"`
		} `json:"components"`
	}
	require.NoError(t, json.Unmarshal(encoded, &document))
	require.NotEmpty(t, document.Components.Schemas[name].Enum, "component %s has no enum", name)
	return document.Components.Schemas[name].Enum
}

// TestJobContractEnumsMatchTheCatalog keeps the published Job type, stage,
// and failure-code enums equal to the descriptors the server registers, so a
// new Job or stage fails here until the contract lists it.
func TestJobContractEnumsMatchTheCatalog(t *testing.T) {
	t.Parallel()
	descriptors := testDescriptors(contractOnboardingService{})
	var types, stages, codes []string
	for _, descriptor := range descriptors {
		types = append(types, string(descriptor.Type))
		stages = append(stages, descriptor.ProgressStages...)
		codes = append(codes, descriptor.PublicErrorCodes...)
	}
	sorted := func(values []string) []string {
		slices.Sort(values)
		return slices.Compact(values)
	}
	jobTypes := make([]string, 0, len(model.JobTypes()))
	for _, jobType := range model.JobTypes() {
		jobTypes = append(jobTypes, string(jobType))
	}
	require.Equal(t, sorted(jobTypes), sorted(types), "every Job type has one registered descriptor")
	require.Equal(t, sorted(types), contractComponentEnum(t, "JobType"))
	require.Equal(t, sorted(stages), contractComponentEnum(t, "JobStage"))
	require.Equal(t, sorted(codes), contractComponentEnum(t, "JobErrorCode"))
}
