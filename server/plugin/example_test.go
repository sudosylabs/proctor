// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package plugin_test

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/sudosylabs/proctor/server/plugin"
)

// recordsOnly is a minimal export format for an archive with no files: it
// writes the records document. A real format must also read every file.
type recordsOnly struct{}

var _ plugin.ExportFormat = recordsOnly{}

func (recordsOnly) MediaType() string { return "application/json" }

func (recordsOnly) FileExtension() string { return ".json" }

func (recordsOnly) Write(ctx context.Context, archive plugin.ExportArchive, w io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := w.Write(archive.Records)
	return err
}

func Example() {
	var format plugin.ExportFormat = recordsOnly{}
	var out bytes.Buffer
	archive := plugin.ExportArchive{Records: []byte(`{"records":[]}`)}
	if err := format.Write(context.Background(), archive, &out); err != nil {
		fmt.Println(err)
		return
	}

	fmt.Println(format.MediaType(), format.FileExtension(), out.String())
	// Output:
	// application/json .json {"records":[]}
}
