package gateway

import (
	"errors"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/pkg/protocolconvert"
	"github.com/kingfs/Trajecta/pkg/secretmask"
)

type fakeConverter struct {
	in       []byte
	out      []byte
	warnings []protocolconvert.Warning
	path     string
	err      error
}

func (f *fakeConverter) ConvertRequest(body []byte) ([]byte, []protocolconvert.Warning, error) {
	f.in = append([]byte(nil), body...)
	if f.err != nil {
		return nil, nil, f.err
	}
	return f.out, f.warnings, nil
}
func (f *fakeConverter) UpstreamPath() (string, error) { return f.path, nil }

type fakeMasker struct {
	events []secretmask.MaskEvent
	called bool
}

func (f *fakeMasker) Mask(body []byte) ([]byte, []secretmask.MaskEvent) {
	f.called = true
	if len(f.events) == 0 {
		return body, nil
	}
	return []byte(strings.ReplaceAll(string(body), "secret", "xxxxxx")), f.events
}

func TestRequestTransformPassthrough(t *testing.T) {
	client := []byte(`{"model":"m"}`)
	result, err := (RequestTransform{RequestedModel: "m"}).Apply(client)
	if err != nil {
		t.Fatal(err)
	}
	if string(result.RecordBody) != string(client) || string(result.WireBody) != string(client) {
		t.Fatalf("result = %q/%q", result.RecordBody, result.WireBody)
	}
	if result.AliasApplied || result.ConversionApplied || result.Masked {
		t.Fatalf("unexpected flags: %+v", result)
	}
	if result.Body == nil || string(result.Body.Client) != string(client) {
		t.Fatalf("Body = %+v", result.Body)
	}
}

func TestRequestTransformAliasThenConversionThenMask(t *testing.T) {
	converter := &fakeConverter{out: []byte(`CONVERTED`)}
	masker := &fakeMasker{events: []secretmask.MaskEvent{{Start: 0, End: 1}}}
	client := []byte(`{"model":"alias","x":1}`)
	result, err := (RequestTransform{
		RequestedModel: "alias",
		UpstreamModel:  "wire",
		ConverterFor: func(record []byte) (Converter, error) {
			// Alias must already be applied when the converter is built.
			if !strings.Contains(string(record), `"model":"wire"`) {
				t.Fatalf("converter saw pre-alias body: %s", record)
			}
			return converter, nil
		},
		Masker: masker,
	}).Apply(client)
	if err != nil {
		t.Fatal(err)
	}
	if !result.AliasApplied || !result.ConversionApplied || !result.Masked {
		t.Fatalf("flags = %+v", result)
	}
	if string(result.RecordBody) != string(client) {
		t.Fatalf("record body = %s, want the original client body %s", result.RecordBody, client)
	}
	if result.Body == nil || string(result.Body.Client) != string(client) {
		t.Fatalf("Body.Client = %+v, want original client bytes %s", result.Body, client)
	}
	if strings.Contains(string(result.RecordBody), "CONVERTED") {
		t.Fatal("record body was converted")
	}
	if string(result.WireBody) != "CONVERTED" {
		t.Fatalf("wire body = %q", result.WireBody)
	}
	if !masker.called {
		t.Fatal("masker not called")
	}
}

func TestRequestTransformKeepsOriginalBodySeparateFromPreprocessedInput(t *testing.T) {
	original := []byte(`{"model":"alias","stream":true}`)
	preprocessed := []byte(`{"model":"alias","stream":true,"stream_options":{"include_usage":true}}`)
	converter := &fakeConverter{out: []byte(`wire body`)}
	result, err := (RequestTransform{
		RequestedModel: "alias",
		UpstreamModel:  "wire",
		OriginalBody:   original,
		ConverterFor: func(aliasBody []byte) (Converter, error) {
			if !strings.Contains(string(aliasBody), `"model":"wire"`) || !strings.Contains(string(aliasBody), `"include_usage":true`) {
				t.Fatalf("converter input = %s, want preprocessed and alias-rewritten body", aliasBody)
			}
			return converter, nil
		},
	}).Apply(preprocessed)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if string(result.RecordBody) != string(original) || string(result.Body.Client) != string(original) {
		t.Fatalf("record/client body = %s/%s, want original %s", result.RecordBody, result.Body.Client, original)
	}
	if string(result.WireBody) != "wire body" {
		t.Fatalf("wire body = %q, want converted body", result.WireBody)
	}
}

func TestRequestTransformConverterErrorStopsBeforeMask(t *testing.T) {
	want := errors.New("unsupported")
	masker := &fakeMasker{}
	_, err := (RequestTransform{
		RequestedModel: "m",
		ConverterFor:   func([]byte) (Converter, error) { return &fakeConverter{err: want}, nil },
		Masker:         masker,
	}).Apply([]byte(`{"model":"m"}`))
	if !errors.Is(err, want) {
		t.Fatalf("err = %v", err)
	}
	if masker.called {
		t.Fatal("masker called after conversion failure")
	}
}

func TestRequestTransformConverterFactoryError(t *testing.T) {
	want := errors.New("no conversion")
	_, err := (RequestTransform{
		RequestedModel: "m",
		ConverterFor:   func([]byte) (Converter, error) { return nil, want },
	}).Apply([]byte(`{"model":"m"}`))
	if !errors.Is(err, want) {
		t.Fatalf("err = %v", err)
	}
}

func TestRequestTransformNoMaskEventsLeavesBody(t *testing.T) {
	client := []byte(`{"model":"m","secret":1}`)
	result, err := (RequestTransform{RequestedModel: "m", Masker: &fakeMasker{}}).Apply(client)
	if err != nil {
		t.Fatal(err)
	}
	if result.Masked || string(result.WireBody) != string(client) {
		t.Fatalf("result = %+v", result)
	}
}
