// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package filecontent

import (
	"bufio"
	"errors"
	"io"
)

// maximumJPEGScans exists because image/jpeg walks the whole coefficient
// buffer once per scan with no limit, so a small progressive file with
// thousands of scans burns minutes of CPU while holding a work slot. Real
// encoders emit at most 18 (libjpeg's CMYK progressive script).
const maximumJPEGScans = 32

var errJPEGTooManyScans = errors.New("JPEG has too many scans")

// checkJPEGScanCount walks markers the way image/jpeg does, in time linear in
// the file size. Malformed or truncated streams are left for the decoder.
func checkJPEGScanCount(r io.Reader) error {
	reader := bufio.NewReader(r)
	var start [2]byte
	if _, err := io.ReadFull(reader, start[:]); err != nil || start != [2]byte{0xff, 0xd8} {
		return nil
	}
	scans := 0
	for {
		marker, err := nextJPEGMarker(reader)
		if err != nil || marker == 0xd9 {
			return nil
		}
		var length [2]byte
		if _, err := io.ReadFull(reader, length[:]); err != nil {
			return nil
		}
		n := int(length[0])<<8 | int(length[1])
		if n < 2 {
			return nil
		}
		if marker == 0xda {
			scans++
			if scans > maximumJPEGScans {
				return errJPEGTooManyScans
			}
		}
		if _, err := reader.Discard(n - 2); err != nil {
			return nil
		}
	}
}

// nextJPEGMarker returns the next marker that image/jpeg would act on. Like
// the decoder it skips stray bytes, 0xff fill bytes, stuffed 0xff00 pairs and
// restart markers, which also skips a scan's entropy-coded data.
func nextJPEGMarker(reader *bufio.Reader) (byte, error) {
	for {
		value, err := reader.ReadByte()
		if err != nil {
			return 0, err
		}
		if value != 0xff {
			continue
		}
		for value == 0xff {
			if value, err = reader.ReadByte(); err != nil {
				return 0, err
			}
		}
		if value == 0x00 || (value >= 0xd0 && value <= 0xd7) {
			continue
		}
		return value, nil
	}
}
