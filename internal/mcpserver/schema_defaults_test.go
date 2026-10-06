package mcpserver

import (
	"reflect"
	"strings"
	"testing"
)

// TestMCPReanalysisSchemaMatchesHandlerDefaults keeps the agent-visible jsonschema text in step with
// what the handlers actually do.
//
// The MCP input structs are the contract an LLM client reads: a wrong default there changes what the
// model believes it asked for. `reanalyzeSessionInput.Async` claimed "default true" while the handler
// branches on `if in.Async`, so an omitted field runs synchronously and returns the result - the
// opposite of the schema text, and the opposite of what docs/MCP_GUIDE.md:107 and :122 document. The
// reparse/scan pair documents "default true" and really does default to both (defaultReanalysisSteps
// returns true/true when neither was asked for), which this gate measures instead of trusting the text.
func TestMCPReanalysisSchemaMatchesHandlerDefaults(t *testing.T) {
	reparse, scan := defaultReanalysisSteps(false, false)
	if !reparse || !scan {
		t.Fatalf("defaultReanalysisSteps(false, false) = %v/%v, want true/true: omitted reparse and scan mean both steps", reparse, scan)
	}
	if onlyReparse, onlyScan := defaultReanalysisSteps(true, false); !onlyReparse || onlyScan {
		t.Fatalf("defaultReanalysisSteps(true, false) = %v/%v, want true/false: an explicit true must not be widened", onlyReparse, onlyScan)
	}

	for _, tc := range []struct {
		value     any
		typeName  string
		schemaFor func(string) string
	}{
		{value: reanalyzeTraceInput{}, typeName: "reanalyzeTraceInput"},
		{value: reanalyzeSessionInput{}, typeName: "reanalyzeSessionInput"},
	} {
		schema := schemaTagsByField(reflect.TypeOf(tc.value))
		for _, field := range []string{"Reparse", "Scan"} {
			tag, ok := schema[field]
			if !ok {
				t.Fatalf("%s has no %s field with a jsonschema tag", tc.typeName, field)
			}
			if !strings.Contains(tag, "default true") {
				t.Fatalf("%s.%s schema = %q, want it to claim `default true`: defaultReanalysisSteps turns an omitted pair into both steps", tc.typeName, field, tag)
			}
		}
		asyncTag, ok := schema["Async"]
		if !ok {
			t.Fatalf("%s has no Async field with a jsonschema tag", tc.typeName)
		}
		if strings.Contains(asyncTag, "default true") {
			t.Fatalf("%s.Async schema = %q claims a true default, but the handler only enqueues when Async is set: an omitted field runs synchronously (docs/MCP_GUIDE.md:107)", tc.typeName, asyncTag)
		}
		if !strings.Contains(asyncTag, "default false") {
			t.Fatalf("%s.Async schema = %q, want it to state the false default the handler implements", tc.typeName, asyncTag)
		}
	}

	traceAsync := schemaTagsByField(reflect.TypeOf(reanalyzeTraceInput{}))["Async"]
	sessionAsync := schemaTagsByField(reflect.TypeOf(reanalyzeSessionInput{}))["Async"]
	if traceAsync != sessionAsync {
		t.Fatalf("the two reanalysis tools describe Async differently:\n trace:   %q\n session: %q", traceAsync, sessionAsync)
	}
}

func schemaTagsByField(input reflect.Type) map[string]string {
	out := map[string]string{}
	for index := 0; index < input.NumField(); index++ {
		field := input.Field(index)
		if tag, ok := field.Tag.Lookup("jsonschema"); ok {
			out[field.Name] = tag
		}
	}
	return out
}
