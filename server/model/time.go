// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"encoding/json"
	"fmt"
	"time"
)

// Domain instants are UTC at microsecond precision, what persistence keeps.
// Truncate before comparing or writing so a database round trip cannot move
// an instant and break a transition or concurrency check. Store contracts
// carry time.Time or OptionalTime, never Unix milliseconds, and every wire
// timestamp goes through FormatRFC3339 and ParseRFC3339.

// TimeUTC returns value in UTC, truncated to microseconds; zero stays zero.
// Truncation never moves a deadline later.
func TimeUTC(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.UTC().Truncate(time.Microsecond)
}

// FormatRFC3339 returns the canonical wire form: RFC 3339 UTC at microsecond
// precision without trailing fractional zeros. Every HTTP and realtime
// timestamp uses it.
func FormatRFC3339(value time.Time) string {
	return TimeUTC(value).Format(time.RFC3339Nano)
}

// ParseRFC3339 parses a wire timestamp with any RFC 3339 offset and fractional
// precision, returning it in UTC at domain precision.
func ParseRFC3339(raw string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, err
	}
	return TimeUTC(parsed), nil
}

// NowUTC returns the current instant at domain precision. Application services
// should take an injected clock instead.
func NowUTC() time.Time {
	return TimeUTC(time.Now())
}

// MillisFromTime converts a time to Unix milliseconds for a command or
// document that still carries them; zero maps to 0.
func MillisFromTime(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UTC().UnixMilli()
}

// TimeFromMillis converts Unix milliseconds to UTC; a non-positive value maps
// to the zero time.
func TimeFromMillis(millis int64) time.Time {
	if millis <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(millis).UTC()
}

// OptionalTime is an optional UTC instant for fields such as archived_at.
// Valid false means absent; a zero Time with Valid true is still a recorded
// instant.
type OptionalTime struct {
	Time  time.Time
	Valid bool
}

// IsSet reports whether the optional instant is present.
func (o OptionalTime) IsSet() bool { return o.Valid }

// OptionalTimeFrom marks value present, even a zero time; use OptionalTime{}
// for absence.
func OptionalTimeFrom(value time.Time) OptionalTime {
	return OptionalTime{Time: TimeUTC(value), Valid: true}
}

// OptionalTimeFromMillis maps Unix milliseconds; a non-positive value is
// absent.
func OptionalTimeFromMillis(millis int64) OptionalTime {
	if millis <= 0 {
		return OptionalTime{}
	}
	return OptionalTime{Time: time.UnixMilli(millis).UTC(), Valid: true}
}

// Millis returns Unix milliseconds, or 0 when absent.
func (o OptionalTime) Millis() int64 {
	if !o.Valid {
		return 0
	}
	return o.Time.UTC().UnixMilli()
}

// UTC returns the instant in UTC at microsecond precision, keeping absence.
func (o OptionalTime) UTC() OptionalTime {
	if !o.Valid {
		return OptionalTime{}
	}
	return OptionalTimeFrom(o.Time)
}

// IsZero reports absence or a present zero time.
func (o OptionalTime) IsZero() bool {
	return !o.Valid || o.Time.IsZero()
}

// Ptr returns the instant for nullable SQL or DTO fields, or nil when absent.
func (o OptionalTime) Ptr() *time.Time {
	if !o.Valid {
		return nil
	}
	value := TimeUTC(o.Time)
	return &value
}

// OptionalTimeFromPtr maps a nullable pointer; nil is absent.
func OptionalTimeFromPtr(value *time.Time) OptionalTime {
	if value == nil {
		return OptionalTime{}
	}
	return OptionalTimeFrom(*value)
}

// MarshalJSON encodes present times as RFC 3339 UTC strings and absence as null.
func (o OptionalTime) MarshalJSON() ([]byte, error) {
	if !o.Valid {
		return []byte("null"), nil
	}
	return json.Marshal(o.FormatRFC3339())
}

// UnmarshalJSON accepts RFC 3339 strings, null, or empty for absence.
func (o *OptionalTime) UnmarshalJSON(data []byte) error {
	if o == nil {
		return invalidModelError("OptionalTime.UnmarshalJSON", "optional_time", "", "OptionalTime unmarshal target is nil", "")
	}

	if string(data) == "null" || string(data) == `""` {
		*o = OptionalTime{}
		return nil
	}

	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		var instant time.Time
		if err2 := json.Unmarshal(data, &instant); err2 != nil {
			return invalidModelError("OptionalTime.UnmarshalJSON", "optional_time", "", "invalid optional time", "").wrap(err)
		}

		*o = OptionalTimeFrom(instant)
		return nil
	}

	if raw == "" {
		*o = OptionalTime{}
		return nil
	}

	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		parsed, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			return invalidModelError("OptionalTime.UnmarshalJSON", "optional_time", "", fmt.Sprintf("invalid optional time %q", raw), "").wrap(err)
		}
	}

	*o = OptionalTimeFrom(parsed)
	return nil
}

// FormatRFC3339 returns the present instant as RFC 3339 UTC, or "" when absent.
func (o OptionalTime) FormatRFC3339() string {
	if !o.Valid {
		return ""
	}
	return FormatRFC3339(o.Time)
}
