// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package modelgateway

import (
	"bufio"
	"errors"
	"io"
	"strings"
)

const MaximumEventBytes = 1 << 20

// SSEDecoder bounds both single lines and assembled multi-line events.
type SSEDecoder struct {
	scanner *bufio.Scanner
	ended   bool
}

func NewSSEDecoder(reader io.Reader) *SSEDecoder {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), MaximumEventBytes)
	return &SSEDecoder{scanner: scanner}
}
func (d *SSEDecoder) Next() ([]byte, error) {
	if d.ended {
		return nil, io.EOF
	}
	var data strings.Builder
	for d.scanner.Scan() {
		line := d.scanner.Text()
		if line == "" && data.Len() > 0 {
			return []byte(strings.TrimSuffix(data.String(), "\n")), nil
		}
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			value = strings.TrimPrefix(value, " ")
			if data.Len()+len(value)+1 > MaximumEventBytes {
				return nil, errors.New("model event exceeds configured limit")
			}
			data.WriteString(value)
			data.WriteByte('\n')
		}
	}
	d.ended = true
	if err := d.scanner.Err(); err != nil {
		return nil, err
	}
	if data.Len() > 0 {
		return []byte(strings.TrimSuffix(data.String(), "\n")), nil
	}
	return nil, io.EOF
}
