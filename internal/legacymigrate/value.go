package legacymigrate

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Column describes one column of the Postgres target table.
type Column struct {
	Name       string
	DataType   string
	UDTName    string
	Nullable   bool
	HasDefault bool
	IsIdentity bool
}

// Required reports whether the column must always receive a value on insert.
func (c Column) Required() bool {
	return !c.Nullable && !c.HasDefault && !c.IsIdentity
}

// sqlSafeText replaces invalid UTF-8 so Postgres accepts the value as text.
// This mirrors internal/store.sqlSafeText.
func sqlSafeText(value string) string {
	if utf8.ValidString(value) {
		return value
	}
	return strings.ToValidUTF8(value, "\uFFFD")
}

func sqlSafeBytes(value []byte) []byte {
	if len(value) == 0 {
		return value
	}
	return []byte(sqlSafeText(string(value)))
}

func normalizeDataType(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// ConvertValue converts a value read from SQLite into the Go type lib/pq
// expects for the target column. A nil or empty source value becomes SQL NULL.
func ConvertValue(column Column, src any) (any, error) {
	dataType := normalizeDataType(column.DataType)
	if dataType == "" {
		dataType = normalizeDataType(column.UDTName)
	}

	if src == nil {
		return nil, nil
	}

	switch {
	case dataType == "boolean":
		return convertBool(src)
	case dataType == "smallint" || dataType == "integer" || dataType == "bigint":
		return convertInt(src)
	case dataType == "real" || dataType == "double precision":
		return convertFloat(src)
	case dataType == "numeric" || dataType == "decimal" || dataType == "money":
		return convertNumeric(src)
	case dataType == "bytea":
		return convertBytes(src)
	case dataType == "timestamp with time zone" || dataType == "timestamp without time zone" ||
		dataType == "timestamp" || dataType == "timestamptz" || dataType == "date":
		converted, isNull, err := ParseLegacyTimeValue(src)
		if err != nil {
			return nil, err
		}
		if isNull {
			return nil, nil
		}
		if dataType == "date" {
			return converted.Format("2006-01-02"), nil
		}
		return converted, nil
	case dataType == "json" || dataType == "jsonb":
		return convertJSON(src)
	case isTextType(dataType):
		return convertText(src)
	default:
		return convertFallback(src)
	}
}

func isTextType(dataType string) bool {
	switch dataType {
	case "text", "character varying", "character", "varchar", "char", "name", "citext",
		"uuid", "inet", "cidr", "macaddr", "interval", "time without time zone",
		"time with time zone", "xml", "tsvector", "money":
		return true
	}
	return strings.HasPrefix(dataType, "character")
}

func convertBool(src any) (any, error) {
	switch value := src.(type) {
	case bool:
		return value, nil
	case int64:
		return value != 0, nil
	case float64:
		return value != 0, nil
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return nil, nil
		}
		switch strings.ToLower(trimmed) {
		case "1", "t", "true", "yes", "on":
			return true, nil
		case "0", "f", "false", "no", "off":
			return false, nil
		}
		return nil, fmt.Errorf("invalid boolean %q", value)
	case []byte:
		return convertBool(string(value))
	default:
		return nil, fmt.Errorf("unsupported boolean source type %T", src)
	}
}

func convertInt(src any) (any, error) {
	switch value := src.(type) {
	case int64:
		return value, nil
	case int:
		return int64(value), nil
	case float64:
		return int64(value), nil
	case bool:
		if value {
			return int64(1), nil
		}
		return int64(0), nil
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return nil, nil
		}
		parsed, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			asFloat, ferr := strconv.ParseFloat(trimmed, 64)
			if ferr != nil {
				return nil, fmt.Errorf("invalid integer %q", value)
			}
			return int64(asFloat), nil
		}
		return parsed, nil
	case []byte:
		return convertInt(string(value))
	default:
		return nil, fmt.Errorf("unsupported integer source type %T", src)
	}
}

func convertFloat(src any) (any, error) {
	switch value := src.(type) {
	case float64:
		return value, nil
	case int64:
		return float64(value), nil
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return nil, nil
		}
		parsed, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid number %q", value)
		}
		return parsed, nil
	case []byte:
		return convertFloat(string(value))
	default:
		return nil, fmt.Errorf("unsupported number source type %T", src)
	}
}

// convertNumeric keeps strings as strings so that Postgres parses the value
// with full precision instead of going through float64.
func convertNumeric(src any) (any, error) {
	switch value := src.(type) {
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return nil, nil
		}
		return trimmed, nil
	case []byte:
		return convertNumeric(string(value))
	default:
		return convertFloat(src)
	}
}

func convertBytes(src any) (any, error) {
	switch value := src.(type) {
	case []byte:
		return value, nil
	case string:
		if value == "" {
			return nil, nil
		}
		return []byte(value), nil
	case int64:
		return []byte(strconv.FormatInt(value, 10)), nil
	case float64:
		return []byte(strconv.FormatFloat(value, 'f', -1, 64)), nil
	default:
		return nil, fmt.Errorf("unsupported bytea source type %T", src)
	}
}

func convertJSON(src any) (any, error) {
	var text string
	switch value := src.(type) {
	case string:
		text = value
	case []byte:
		if len(value) == 0 {
			return nil, nil
		}
		return sqlSafeBytes(value), nil
	case int64:
		text = strconv.FormatInt(value, 10)
	case float64:
		text = strconv.FormatFloat(value, 'f', -1, 64)
	case bool:
		text = strconv.FormatBool(value)
	default:
		return nil, fmt.Errorf("unsupported json source type %T", src)
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		// An empty string is not valid json/jsonb input, and the current
		// writers use the empty string to mean "no value recorded".
		return nil, nil
	}
	return sqlSafeText(trimmed), nil
}

func convertText(src any) (any, error) {
	switch value := src.(type) {
	case string:
		return sqlSafeText(value), nil
	case []byte:
		return sqlSafeText(string(value)), nil
	case int64:
		return strconv.FormatInt(value, 10), nil
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(value), nil
	case time.Time:
		return value.UTC().Format(time.RFC3339Nano), nil
	default:
		return nil, fmt.Errorf("unsupported text source type %T", src)
	}
}

// convertFallback passes driver-native values through for column types this
// tool does not model explicitly, letting Postgres decide.
func convertFallback(src any) (any, error) {
	switch value := src.(type) {
	case string:
		return sqlSafeText(value), nil
	case []byte:
		return sqlSafeBytes(value), nil
	case int64, float64, bool, time.Time:
		return value, nil
	case nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("unsupported source type %T", src)
	}
}

// ZeroValue returns a placeholder for a required target column that has no
// counterpart in the legacy SQLite schema. It is only used when the operator
// explicitly opts in with --fill-missing-required.
func ZeroValue(column Column) (any, error) {
	dataType := normalizeDataType(column.DataType)
	switch {
	case dataType == "boolean":
		return false, nil
	case dataType == "smallint" || dataType == "integer" || dataType == "bigint":
		return int64(0), nil
	case dataType == "real" || dataType == "double precision":
		return float64(0), nil
	case dataType == "numeric" || dataType == "decimal" || dataType == "money":
		return "0", nil
	case dataType == "bytea":
		return []byte{}, nil
	case dataType == "timestamp with time zone" || dataType == "timestamp without time zone" ||
		dataType == "timestamp" || dataType == "timestamptz":
		return time.Unix(0, 0).UTC(), nil
	case dataType == "date":
		return "1970-01-01", nil
	case dataType == "json" || dataType == "jsonb":
		return "{}", nil
	case isTextType(dataType):
		return "", nil
	default:
		return nil, fmt.Errorf("no placeholder value for column type %q", column.DataType)
	}
}
