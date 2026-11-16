// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package config

import (
	"context"
	"errors"
	"fmt"
	"os"
)

const (
	// DefaultPath is relative to the working directory.
	DefaultPath = "config/config.json"
	// PathEnv selects the configuration file when Open is given no path.
	PathEnv = "PROCTOR_CONFIG"
)

// Open loads the configuration from path, else PROCTOR_CONFIG, else
// DefaultPath. It never creates the file: a missing one is an error.
func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		path = os.Getenv(PathEnv)
	}
	if path == "" {
		path = DefaultPath
	}
	fileStore, err := NewFileStore(path)
	if err != nil {
		return nil, err
	}
	configuration, err := NewStore(ctx, fileStore, StoreOptions{})
	if err != nil && errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf(
			"load required configuration %q (copy config/config.example.json to %q and edit it): %w",
			path,
			path,
			err,
		)
	}
	return configuration, err
}
