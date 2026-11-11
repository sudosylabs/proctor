// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package job

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

// Registry is the closed set of Job types an Engine runs. It is fixed when
// built and hands out copies, so nothing can change a registered Descriptor.
type Registry struct {
	descriptors map[model.JobType]Descriptor
	types       []model.JobType
}

// NewRegistry validates values and refuses an empty list, a duplicate type, a
// missing Handler, a heartbeat interval that is not shorter than the lease,
// failure retention shorter than success retention, and any progress stage,
// public error code or retry status that is malformed or repeated. It stores
// each code list sorted.
func NewRegistry(values []Descriptor) (*Registry, error) {
	if len(values) == 0 {
		return nil, errors.New("job registry requires at least one descriptor")
	}

	registry := &Registry{descriptors: make(map[model.JobType]Descriptor, len(values)), types: make([]model.JobType, 0, len(values))}
	for _, value := range values {
		if value.Handler == nil ||
			value.Timeout <= 0 ||
			value.Concurrency <= 0 ||
			value.MaximumAttempts <= 0 ||
			value.LeaseDuration <= 0 ||
			value.HeartbeatInterval <= 0 ||
			value.HeartbeatInterval >= value.LeaseDuration ||
			value.BaseRetryDelay <= 0 ||
			value.MaximumRetryDelay < value.BaseRetryDelay ||
			(value.Visibility != VisibilityOperator && value.Visibility != VisibilityDomain) ||
			value.SuccessRetention <= 0 ||
			value.FailureRetention < value.SuccessRetention {
			return nil, fmt.Errorf("invalid descriptor for job type %q", value.Type)
		}

		if _, exists := registry.descriptors[value.Type]; exists {
			return nil, fmt.Errorf("duplicate job type %q", value.Type)
		}

		var normalizeErr error
		value.ProgressStages, normalizeErr = normalizedCodes(value.ProgressStages)
		if normalizeErr != nil {
			return nil, fmt.Errorf("invalid progress stages for job type %q: %w", value.Type, normalizeErr)
		}

		value.PublicErrorCodes, normalizeErr = normalizedCodes(value.PublicErrorCodes)
		if normalizeErr != nil {
			return nil, fmt.Errorf("invalid public error codes for job type %q: %w", value.Type, normalizeErr)
		}

		value.ExplicitRetryStatuses, normalizeErr = normalizedTerminalJobStatuses(value.ExplicitRetryStatuses)
		if normalizeErr != nil {
			return nil, fmt.Errorf("invalid explicit retry statuses for job type %q: %w", value.Type, normalizeErr)
		}

		registry.descriptors[value.Type] = value
		registry.types = append(registry.types, value.Type)
	}

	slices.Sort(registry.types)
	return registry, nil
}

// Descriptor returns a copy of the Descriptor registered for jobType,
// including its Handler, or an error when the type is not registered.
func (r *Registry) Descriptor(jobType model.JobType) (Descriptor, error) {
	descriptor, ok := r.descriptors[jobType]
	if !ok {
		return Descriptor{}, fmt.Errorf("job type %q is not registered", jobType)
	}
	descriptor.ProgressStages = append([]string(nil), descriptor.ProgressStages...)
	descriptor.PublicErrorCodes = append([]string(nil), descriptor.PublicErrorCodes...)
	descriptor.ExplicitRetryStatuses = append([]model.JobStatus(nil), descriptor.ExplicitRetryStatuses...)
	return descriptor, nil
}

// Types returns the registered Job types in sorted order.
func (r *Registry) Types() []model.JobType {
	return append([]model.JobType(nil), r.types...)
}

// MaximumTimeout returns the longest Timeout of any registered type, the
// longest an Engine waits for its workers when it closes.
func (r *Registry) MaximumTimeout() time.Duration {
	var maximum time.Duration
	for _, descriptor := range r.descriptors {
		maximum = max(maximum, descriptor.Timeout)
	}
	return maximum
}

func normalizedTerminalJobStatuses(values []model.JobStatus) ([]model.JobStatus, error) {
	statuses := append([]model.JobStatus(nil), values...)
	slices.Sort(statuses)
	for index, status := range statuses {
		if (status != model.JobStatusFailed && status != model.JobStatusCanceled) ||
			(index > 0 && statuses[index-1] == status) {
			return nil, errors.New("statuses must be unique terminal retry states")
		}
	}
	return statuses, nil
}

var jobContractCode = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,127}$`)

func normalizedCodes(values []string) ([]string, error) {
	codes := append([]string(nil), values...)
	slices.Sort(codes)
	for index, code := range codes {
		if !jobContractCode.MatchString(code) || (index > 0 && codes[index-1] == code) {
			return nil, errors.New("codes must be unique safe identifiers")
		}
	}
	return codes, nil
}
