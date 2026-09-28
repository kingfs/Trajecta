package legacymigrate

import (
	"errors"
	"testing"
	"time"
)

func TestParseLegacyTimeAcceptedForms(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want time.Time
	}{
		{
			name: "raw sql RFC3339Nano",
			in:   "2025-12-23T12:17:14.088863521Z",
			want: time.Date(2025, 12, 23, 12, 17, 14, 88863521, time.UTC),
		},
		{
			name: "driver time.Time.String",
			in:   "2026-05-15 01:54:55.069380977 +0000 UTC",
			want: time.Date(2026, 5, 15, 1, 54, 55, 69380977, time.UTC),
		},
		{
			name: "driver time.Time.String with monotonic suffix",
			in:   "2026-04-27 12:56:02.167889738 +0000 UTC m=+0.095068526",
			want: time.Date(2026, 4, 27, 12, 56, 2, 167889738, time.UTC),
		},
		{
			name: "sqlite CURRENT_TIMESTAMP",
			in:   "2026-01-02 03:04:05",
			want: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		},
		{
			name: "offset without zone name",
			in:   "2026-01-02 03:04:05.5 +0800",
			want: time.Date(2026, 1, 1, 19, 4, 5, 500000000, time.UTC),
		},
		{
			name: "date only",
			in:   "2026-01-02",
			want: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "unix seconds",
			in:   "1767225645",
			want: time.Unix(1767225645, 0).UTC(),
		},
		{
			name: "unix milliseconds",
			in:   "1767225645123",
			want: time.UnixMilli(1767225645123).UTC(),
		},
		{
			name: "surrounding whitespace",
			in:   "  2026-01-02 03:04:05  ",
			want: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseLegacyTime(tc.in)
			if err != nil {
				t.Fatalf("ParseLegacyTime(%q) error = %v", tc.in, err)
			}
			if !got.Equal(tc.want) {
				t.Errorf("ParseLegacyTime(%q) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseLegacyTimeRejected(t *testing.T) {
	if _, err := ParseLegacyTime(""); !errors.Is(err, ErrEmptyLegacyTime) {
		t.Errorf("ParseLegacyTime(\"\") error = %v, want ErrEmptyLegacyTime", err)
	}
	for _, in := range []string{"not a timestamp", "2026-13-45 99:99:99", "12/31/2026"} {
		if got, err := ParseLegacyTime(in); err == nil {
			t.Errorf("ParseLegacyTime(%q) = %s, want an error", in, got)
		}
	}
}

func TestParseLegacyTimeValue(t *testing.T) {
	t.Run("nil is null", func(t *testing.T) {
		got, isNull, err := ParseLegacyTimeValue(nil)
		if err != nil || !isNull || !got.IsZero() {
			t.Fatalf("ParseLegacyTimeValue(nil) = (%s, %v, %v), want a null result", got, isNull, err)
		}
	})
	t.Run("empty string is null", func(t *testing.T) {
		if _, isNull, err := ParseLegacyTimeValue("   "); err != nil || !isNull {
			t.Fatalf("ParseLegacyTimeValue(blank) = (_, %v, %v), want a null result", isNull, err)
		}
	})
	t.Run("time.Time passes through in UTC", func(t *testing.T) {
		local := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("CST", 8*3600))
		got, isNull, err := ParseLegacyTimeValue(local)
		if err != nil || isNull {
			t.Fatalf("ParseLegacyTimeValue(time.Time) = (_, %v, %v), want a value", isNull, err)
		}
		if !got.Equal(local) || got.Location() != time.UTC {
			t.Errorf("ParseLegacyTimeValue(time.Time) = %s (%s), want %s in UTC", got, got.Location(), local.UTC())
		}
	})
	t.Run("bytes", func(t *testing.T) {
		got, isNull, err := ParseLegacyTimeValue([]byte("2026-01-02 03:04:05"))
		if err != nil || isNull {
			t.Fatalf("ParseLegacyTimeValue([]byte) = (_, %v, %v), want a value", isNull, err)
		}
		if want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC); !got.Equal(want) {
			t.Errorf("ParseLegacyTimeValue([]byte) = %s, want %s", got, want)
		}
	})
	t.Run("unix integer", func(t *testing.T) {
		got, _, err := ParseLegacyTimeValue(int64(1767225645))
		if err != nil {
			t.Fatalf("ParseLegacyTimeValue(int64) error = %v", err)
		}
		if want := time.Unix(1767225645, 0).UTC(); !got.Equal(want) {
			t.Errorf("ParseLegacyTimeValue(int64) = %s, want %s", got, want)
		}
	})
	t.Run("unsupported type", func(t *testing.T) {
		if _, _, err := ParseLegacyTimeValue(struct{}{}); err == nil {
			t.Error("ParseLegacyTimeValue(struct{}) error = nil, want an error")
		}
	})
}
