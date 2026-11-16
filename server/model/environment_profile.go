// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"regexp"
	"time"
)

// The bounds of an environment profile.
const (
	EnvironmentProfileMinimumCPUMillis   = 100
	EnvironmentProfileMaximumCPUMillis   = 64_000
	EnvironmentProfileMinimumMemoryBytes = 128 << 20
	EnvironmentProfileMaximumMemoryBytes = 64 << 30
	EnvironmentProfileMinimumDiskBytes   = 64 << 20
	EnvironmentProfileMaximumDiskBytes   = 256 << 30
	EnvironmentProfileMinimumIdleWindow  = time.Minute
	EnvironmentProfileMaximumIdleWindow  = 24 * time.Hour
	// EnvironmentProfileMaximumTerminals bounds the terminals one Attempt
	// may open, whatever a profile allows.
	EnvironmentProfileMaximumTerminals = 8
	// EnvironmentProfileListMaximum bounds how many profiles an
	// installation keeps that are not retired.
	EnvironmentProfileListMaximum = 200
)

var (
	environmentProfileNamePattern  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	environmentProfileImagePattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
)

// EnvironmentNetwork is what network an Attempt Environment gets.
type EnvironmentNetwork string

const (
	// EnvironmentNetworkNone gives the machine no network.
	EnvironmentNetworkNone EnvironmentNetwork = "none"
	// EnvironmentNetworkAllowlist gives the machine the execution host's
	// configured allowlist and nothing else.
	EnvironmentNetworkAllowlist EnvironmentNetwork = "allowlist"
)

func (n EnvironmentNetwork) IsValid() bool {
	return n == EnvironmentNetworkNone || n == EnvironmentNetworkAllowlist
}

// EnvironmentResources is what one Attempt Environment reserves on its host.
type EnvironmentResources struct {
	CPUMillis   int64
	MemoryBytes int64
	DiskBytes   int64
}

// Valid reports whether every resource is within the profile bounds.
func (r EnvironmentResources) Valid() bool {
	return r.CPUMillis >= EnvironmentProfileMinimumCPUMillis && r.CPUMillis <= EnvironmentProfileMaximumCPUMillis &&
		r.MemoryBytes >= EnvironmentProfileMinimumMemoryBytes && r.MemoryBytes <= EnvironmentProfileMaximumMemoryBytes &&
		r.DiskBytes >= EnvironmentProfileMinimumDiskBytes && r.DiskBytes <= EnvironmentProfileMaximumDiskBytes
}

// EnvironmentProfile is an installation-level, administrator-managed recipe
// for Attempt Environments: the execution image, the resources, the network,
// how long an environment may stay frozen before it is suspended, and the
// most terminals one Attempt may open. A profile is never edited: a change
// is a new profile, and the old one is retired. An Exam's execution policy
// names one profile, and Revisions published before a retirement keep using
// it.
type EnvironmentProfile struct {
	ID              EnvironmentProfileID
	Name            string
	Image           string
	Resources       EnvironmentResources
	Network         EnvironmentNetwork
	IdleWindow      time.Duration
	MaxTerminals    int
	CreatedByUserID UserID
	CreatedAt       time.Time
	// RetiredAt is zero while new Exam Revisions may name the profile.
	RetiredAt time.Time
}

// EnvironmentProfileSpecification is what an administrator gives for a new
// profile.
type EnvironmentProfileSpecification struct {
	Name         string
	Image        string
	Resources    EnvironmentResources
	Network      EnvironmentNetwork
	IdleWindow   time.Duration
	MaxTerminals int
}

// NewEnvironmentProfile validates spec and returns the profile it makes.
func NewEnvironmentProfile(
	id EnvironmentProfileID,
	spec EnvironmentProfileSpecification,
	createdBy UserID,
	at time.Time,
) (*EnvironmentProfile, error) {
	profile := &EnvironmentProfile{
		ID:              id,
		Name:            spec.Name,
		Image:           spec.Image,
		Resources:       spec.Resources,
		Network:         spec.Network,
		IdleWindow:      spec.IdleWindow,
		MaxTerminals:    spec.MaxTerminals,
		CreatedByUserID: createdBy,
		CreatedAt:       TimeUTC(at),
	}
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	return profile, nil
}

// Validate checks every field.
func (p *EnvironmentProfile) Validate() error {
	const where = "EnvironmentProfile.Validate"
	switch {
	case p == nil || !p.ID.IsValid():
		return invalidModelError(where, "environment_profile", "id", "must be a valid identifier", "")
	case !environmentProfileNamePattern.MatchString(p.Name):
		return invalidModelError(where, "environment_profile", "name",
			"must be a lowercase letter, then up to 62 lowercase letters, digits or hyphens", "")
	case !environmentProfileImagePattern.MatchString(p.Image):
		return invalidModelError(where, "environment_profile", "image",
			"must be 1 to 64 letters, digits, dots, underscores, colons or hyphens", "")
	case !p.Resources.Valid():
		return invalidModelError(where, "environment_profile", "resources", "are out of bounds", "")
	case !p.Network.IsValid():
		return invalidModelError(where, "environment_profile", "network", "must be none or allowlist", "")
	case p.IdleWindow < EnvironmentProfileMinimumIdleWindow || p.IdleWindow > EnvironmentProfileMaximumIdleWindow ||
		p.IdleWindow%time.Second != 0:
		return invalidModelError(where, "environment_profile", "idle_window",
			"must be whole seconds from one minute to 24 hours", "")
	case p.MaxTerminals < 1 || p.MaxTerminals > EnvironmentProfileMaximumTerminals:
		return invalidModelError(where, "environment_profile", "max_terminals", "must be from 1 to 8", "")
	case !p.CreatedByUserID.IsValid() || p.CreatedAt.IsZero():
		return invalidModelError(where, "environment_profile", "created", "requires a creator and a time", "")
	case !p.RetiredAt.IsZero() && p.RetiredAt.Before(p.CreatedAt):
		return invalidModelError(where, "environment_profile", "retired_at", "cannot precede creation", "")
	}
	return nil
}

// Retired reports whether new Exam Revisions may no longer name the profile.
func (p *EnvironmentProfile) Retired() bool { return !p.RetiredAt.IsZero() }
