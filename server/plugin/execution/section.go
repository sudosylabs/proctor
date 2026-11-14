// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/sudosylabs/execenv"
)

// Section is the Exam policy section an author sets to give candidates a
// terminal. The network is always none in this version, so it is not a
// field.
type Section struct {
	// Image is the execenv catalog Image every candidate's grant boots.
	Image string `json:"image"`
	// RecordInput records the candidate's terminal input as evidence.
	RecordInput bool `json:"record_input,omitempty"`
}

var imagePattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

// errInvalidSection refuses a section; the author never sees its text.
var errInvalidSection = errors.New("execution section is invalid")

func parseSection(document json.RawMessage) (Section, error) {
	var section Section
	if err := decodeStrict(document, &section); err != nil {
		return Section{}, fmt.Errorf("%w: %w", errInvalidSection, err)
	}
	if !imagePattern.MatchString(section.Image) {
		return Section{}, fmt.Errorf("%w: image name", errInvalidSection)
	}
	return section, nil
}

var sectionSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["image"],
  "properties": {
    "image": {
      "type": "string", "pattern": "^[A-Za-z0-9._:-]{1,64}$",
      "title": "Image",
      "description": "The environment image every candidate's terminal starts from, as the execution hosts name it."
    },
    "record_input": {
      "type": "boolean", "default": false,
      "title": "Record terminal input",
      "description": "Record what candidates type and paste into the terminal, with its timing, for reviewers. Output is never recorded."
    }
  }
}`)

// examSection is the plugin's plugin.ExamSection.
type examSection struct{ plugin *Plugin }

func (examSection) Schema() json.RawMessage { return append(json.RawMessage(nil), sectionSchema...) }

// ValidateSection accepts a section whose image at least one usable host
// offers now. With no host reachable it refuses, so publication never
// passes on stale capacity data.
func (s examSection) ValidateSection(ctx context.Context, document json.RawMessage) error {
	section, err := parseSection(document)
	if err != nil {
		return err
	}
	pool := s.plugin.pool()
	if pool == nil {
		return fmt.Errorf("%w: no execution hosts are configured", errInvalidSection)
	}
	reports := pool.reports(ctx, true)
	if len(reports) == 0 {
		return fmt.Errorf("%w: no execution host is reachable", errInvalidSection)
	}
	for _, report := range reports {
		if report.usable(execenv.Image(section.Image)) {
			return nil
		}
	}
	return fmt.Errorf("%w: no usable host offers the image", errInvalidSection)
}
