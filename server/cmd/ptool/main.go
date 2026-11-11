// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Command ptool is the repository's developer tool. It generates and checks
// the error catalog and the OpenAPI artifact, checks translation catalogs,
// seeds a development installation and builds release artifacts. The Makefile
// targets call it; it is not shipped to operators.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/sudosylabs/proctor/server/cmd/ptool/commands"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := commands.Execute(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
