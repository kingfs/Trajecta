package legacymigrate

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ErrEmptyLegacyTime is returned when a timestamp column holds an empty value.
var ErrEmptyLegacyTime = errors.New("empty timestamp value")

// legacyTimeLayouts covers every encoding observed in pre-rename SQLite
// application databases, plus the ISO forms the current raw-SQL writers emit.
//
// The pre-rename writers stored Go time.Time values through the SQLite driver,
// which renders them with time.Time.String(). Values that came from time.Now()
// keep their monotonic clock reading, so real legacy files contain strings like
// "2026-04-27 12:56:02.167889738 +0000 UTC m=+0.095068526".
var legacyTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.999999999 -0700 MST",
	"2006-01-02 15:04:05.999999999 -0700",
	"2006-01-02 15:04:05 -0700 MST",
	"2006-01-02 15:04:05 -0700",
	"2006-01-02 15:04:05.999999999 -07:00 MST",
	"2006-01-02 15:04:05.999999999 -07:00",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02",
	"15:04:05",
}

// ParseLegacyTime parses a timestamp read from a legacy SQLite database and
// normalizes it to UTC.
func ParseLegacyTime(raw string) (time.Time, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return time.Time{}, ErrEmptyLegacyTime
	}
	value = stripMonotonic(value)
	for _, layout := range legacyTimeLayouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	if parsed, ok := parseUnixMagnitude(value); ok {
		return parsed, nil
	}
	return time.Time{}, fmt.Errorf("unrecognized timestamp %q", raw)
}

// stripMonotonic removes the " m=+1.234" wall-clock reading suffix that
// time.Time.String() appends for values carrying a monotonic clock.
func stripMonotonic(value string) string {
	if idx := strings.Index(value, " m="); idx >= 0 {
		return strings.TrimSpace(value[:idx])
	}
	return value
}

// parseUnixMagnitude interprets a bare integer timestamp by its digit count.
func parseUnixMagnitude(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	negative := strings.HasPrefix(value, "-")
	digits := strings.TrimPrefix(value, "-")
	for _, r := range digits {
		if r < '0' || r > '9' {
			return time.Time{}, false
		}
	}
	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	var parsed time.Time
	switch len(digits) {
	case 10:
		parsed = time.Unix(number, 0)
	case 13:
		parsed = time.UnixMilli(number)
	case 16:
		parsed = time.UnixMicro(number)
	case 19:
		parsed = time.Unix(0, number)
	default:
		if negative {
			return time.Time{}, false
		}
		parsed = time.Unix(number, 0)
	}
	return parsed.UTC(), true
}

// ParseLegacyTimeValue accepts any driver value for a timestamp column.
func ParseLegacyTimeValue(src any) (time.Time, bool, error) {
	switch value := src.(type) {
	case nil:
		return time.Time{}, true, nil
	case time.Time:
		return value.UTC(), false, nil
	case string:
		if strings.TrimSpace(value) == "" {
			return time.Time{}, true, nil
		}
		parsed, err := ParseLegacyTime(value)
		return parsed, false, err
	case []byte:
		if len(value) == 0 {
			return time.Time{}, true, nil
		}
		parsed, err := ParseLegacyTime(string(value))
		return parsed, false, err
	case int64:
		return time.Unix(value, 0).UTC(), false, nil
	case float64:
		seconds := int64(value)
		nanos := int64((value - float64(seconds)) * float64(time.Second))
		return time.Unix(seconds, nanos).UTC(), false, nil
	default:
		return time.Time{}, false, fmt.Errorf("unsupported timestamp value type %T", src)
	}
}
