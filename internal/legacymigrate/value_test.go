package legacymigrate

import (
	"bytes"
	"testing"
	"time"
	"unicode/utf8"
)

func TestConvertValueScalars(t *testing.T) {
	cases := []struct {
		name   string
		column Column
		src    any
		want   any
	}{
		{"bool from integer zero", Column{Name: "enabled", DataType: "boolean"}, int64(0), false},
		{"bool from integer one", Column{Name: "enabled", DataType: "boolean"}, int64(1), true},
		{"bool from text", Column{Name: "enabled", DataType: "boolean"}, "on", true},
		{"bool from empty text is null", Column{Name: "enabled", DataType: "boolean"}, "", nil},
		{"bigint from text", Column{Name: "total_tokens", DataType: "bigint"}, "42", int64(42)},
		{"bigint from float", Column{Name: "total_tokens", DataType: "bigint"}, float64(7.9), int64(7)},
		{"numeric keeps precision", Column{Name: "amount", DataType: "numeric"}, "0.1234567890123456789", "0.1234567890123456789"},
		{"double from integer", Column{Name: "ratio", DataType: "double precision"}, int64(3), float64(3)},
		{"text from integer", Column{Name: "model", DataType: "character varying"}, int64(5), "5"},
		{"text from time", Column{Name: "model", DataType: "text"}, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), "2026-01-02T03:04:05Z"},
		{"uuid is text", Column{Name: "id", DataType: "uuid"}, "abc", "abc"},
		{"json keeps object", Column{Name: "payload", DataType: "jsonb"}, `{"a":1}`, `{"a":1}`},
		{"json empty text is null", Column{Name: "payload", DataType: "jsonb"}, "", nil},
		{"nil is null", Column{Name: "model", DataType: "text"}, nil, nil},
		{"unknown type falls back", Column{Name: "shape", DataType: "point"}, "1,2", "1,2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ConvertValue(tc.column, tc.src)
			if err != nil {
				t.Fatalf("ConvertValue(%s, %#v) error = %v", tc.column.DataType, tc.src, err)
			}
			if !valuesEqual(got, tc.want) {
				t.Errorf("ConvertValue(%s, %#v) = %#v, want %#v", tc.column.DataType, tc.src, got, tc.want)
			}
		})
	}
}

func TestConvertValueTimestamps(t *testing.T) {
	column := Column{Name: "recorded_at", DataType: "timestamp with time zone"}
	for _, raw := range []string{
		"2025-12-23T12:17:14.088863521Z",
		"2026-05-15 01:54:55.069380977 +0000 UTC",
		"2026-04-27 12:56:02.167889738 +0000 UTC m=+0.095068526",
	} {
		got, err := ConvertValue(column, raw)
		if err != nil {
			t.Fatalf("ConvertValue(timestamptz, %q) error = %v", raw, err)
		}
		if _, ok := got.(time.Time); !ok {
			t.Errorf("ConvertValue(timestamptz, %q) = %#v, want a time.Time", raw, got)
		}
	}

	date := Column{Name: "day", DataType: "date"}
	got, err := ConvertValue(date, "2026-04-27 12:56:02.167889738 +0000 UTC m=+0.095068526")
	if err != nil {
		t.Fatalf("ConvertValue(date) error = %v", err)
	}
	if got != "2026-04-27" {
		t.Errorf("ConvertValue(date) = %#v, want 2026-04-27", got)
	}
}

func TestConvertValueErrors(t *testing.T) {
	if _, err := ConvertValue(Column{Name: "enabled", DataType: "boolean"}, "maybe"); err == nil {
		t.Error("ConvertValue(boolean, maybe) error = nil, want an error")
	}
	if _, err := ConvertValue(Column{Name: "recorded_at", DataType: "timestamptz"}, "not a time"); err == nil {
		t.Error("ConvertValue(timestamptz, not a time) error = nil, want an error")
	}
	if _, err := ConvertValue(Column{Name: "total", DataType: "bigint"}, "12.5.5"); err == nil {
		t.Error("ConvertValue(bigint, 12.5.5) error = nil, want an error")
	}
}

func TestConvertValueSanitizesInvalidUTF8(t *testing.T) {
	invalid := string([]byte{'a', 0xff, 'b'})
	got, err := ConvertValue(Column{Name: "model", DataType: "text"}, invalid)
	if err != nil {
		t.Fatalf("ConvertValue(text, invalid utf8) error = %v", err)
	}
	text, ok := got.(string)
	if !ok {
		t.Fatalf("ConvertValue(text, invalid utf8) = %#v, want a string", got)
	}
	if !utf8.ValidString(text) {
		t.Errorf("ConvertValue(text) returned invalid UTF-8 %q", text)
	}
}

func TestConvertValueByteaMatchesColumnTextGuard(t *testing.T) {
	payload := []byte{0x00, 0x01, 0xff}
	got, err := ConvertValue(Column{Name: "blob", DataType: "bytea"}, payload)
	if err != nil {
		t.Fatalf("ConvertValue(bytea) error = %v", err)
	}
	bytesValue, ok := got.([]byte)
	if !ok || !bytes.Equal(bytesValue, payload) {
		t.Errorf("ConvertValue(bytea) = %#v, want %#v", got, payload)
	}
}

func TestZeroValue(t *testing.T) {
	cases := []struct {
		column Column
		want   any
	}{
		{Column{Name: "enabled", DataType: "boolean"}, false},
		{Column{Name: "count", DataType: "bigint"}, int64(0)},
		{Column{Name: "amount", DataType: "numeric"}, "0"},
		{Column{Name: "recorded_at", DataType: "timestamptz"}, time.Unix(0, 0).UTC()},
		{Column{Name: "day", DataType: "date"}, "1970-01-01"},
		{Column{Name: "payload", DataType: "jsonb"}, "{}"},
		{Column{Name: "model", DataType: "character varying"}, ""},
	}
	for _, tc := range cases {
		got, err := ZeroValue(tc.column)
		if err != nil {
			t.Fatalf("ZeroValue(%s) error = %v", tc.column.DataType, err)
		}
		if !valuesEqual(got, tc.want) {
			t.Errorf("ZeroValue(%s) = %#v, want %#v", tc.column.DataType, got, tc.want)
		}
	}
	if _, err := ZeroValue(Column{Name: "shape", DataType: "point"}); err == nil {
		t.Error("ZeroValue(point) error = nil, want an error")
	}
}

func TestColumnRequired(t *testing.T) {
	cases := []struct {
		column Column
		want   bool
	}{
		{Column{Name: "path", DataType: "text"}, true},
		{Column{Name: "provider", DataType: "text", HasDefault: true}, false},
		{Column{Name: "notes", DataType: "text", Nullable: true}, false},
		{Column{Name: "id", DataType: "bigint", IsIdentity: true}, false},
	}
	for _, tc := range cases {
		if got := tc.column.Required(); got != tc.want {
			t.Errorf("Column%+v.Required() = %v, want %v", tc.column, got, tc.want)
		}
	}
}

// TestSQLSafeTextStripsNUL covers the byte Postgres rejects with `22021 invalid
// byte sequence for encoding "UTF8": 0x00`. NUL is valid UTF-8, so a plain
// utf8.ValidString guard is not enough; legacy text_preview/raw values really do
// contain it. Invalid UTF-8 keeps the historical U+FFFD substitution.
func TestSQLSafeTextStripsNUL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "clean text is unchanged", in: "plain text", want: "plain text"},
		{name: "nul bytes are dropped", in: "a\x00b", want: "ab"},
		{name: "leading and trailing nul", in: "\x00ab\x00", want: "ab"},
		{name: "only nul", in: "\x00", want: ""},
		{name: "nul and invalid utf8", in: "a\x00\xffb", want: "a\uFFFDb"},
		{name: "invalid utf8 is replaced", in: "a\xffb", want: "a\uFFFDb"},
		{name: "valid multibyte survives", in: "中文", want: "中文"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sqlSafeText(tc.in)
			if got != tc.want {
				t.Fatalf("sqlSafeText(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("sqlSafeText(%q) = %q is not valid UTF-8", tc.in, got)
			}
		})
	}
}

// TestConvertValueSanitizesTextAndJSONNUL pins that the sanitizing happens on
// the conversion paths the merge uses, not only in the helper.
func TestConvertValueSanitizesTextAndJSONNUL(t *testing.T) {
	text := Column{Name: "text_preview", DataType: "text"}
	got, err := ConvertValue(text, "before\x00after")
	if err != nil {
		t.Fatalf("ConvertValue(text) error: %v", err)
	}
	if got != "beforeafter" {
		t.Fatalf("ConvertValue(text) = %q, want %q", got, "beforeafter")
	}

	jsonb := Column{Name: "json", DataType: "jsonb"}
	payload := "{\"text\":\"a\x00b\"}"
	got, err = ConvertValue(jsonb, payload)
	if err != nil {
		t.Fatalf("ConvertValue(jsonb) error: %v", err)
	}
	if got != "{\"text\":\"ab\"}" {
		t.Fatalf("ConvertValue(jsonb) = %q, want %q", got, "{\"text\":\"ab\"}")
	}
}

func valuesEqual(got, want any) bool {
	switch expected := want.(type) {
	case []byte:
		actual, ok := got.([]byte)
		return ok && bytes.Equal(actual, expected)
	case time.Time:
		actual, ok := got.(time.Time)
		return ok && actual.Equal(expected)
	default:
		return got == want
	}
}
