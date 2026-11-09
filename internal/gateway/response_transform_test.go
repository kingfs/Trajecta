package gateway

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/pkg/protocolconvert"
)

type fakeResponseConverter struct {
	calls    int
	warnings []protocolconvert.Warning
	err      error
}

func (f *fakeResponseConverter) ConvertResponse(body []byte) ([]byte, []protocolconvert.Warning, error) {
	f.calls++
	if f.err != nil {
		return nil, nil, f.err
	}
	return bytes.ToUpper(body), f.warnings, nil
}

func responseWith(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Encoding": []string{"gzip"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestResponseTransformConvertsAndRewritesHeaders(t *testing.T) {
	converter := &fakeResponseConverter{warnings: []protocolconvert.Warning{{Field: "x", Reason: "loss"}}}
	resp, warnings, err := (ResponseTransform{Converter: converter}).Apply(responseWith(200, `{"ok":true}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"OK":TRUE}` {
		t.Fatalf("body = %q", body)
	}
	if resp.ContentLength != int64(len(body)) || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("envelope = %+v", resp)
	}
	if resp.Header.Get("Content-Encoding") != "" {
		t.Fatal("content-encoding not removed")
	}
	if len(warnings) != 1 || warnings[0].Reason != "loss" {
		t.Fatalf("warnings = %+v", warnings)
	}
}

func TestResponseTransformPassesThroughNon2xxAndEmpty(t *testing.T) {
	converter := &fakeResponseConverter{}
	errResp := responseWith(502, `{"error":"boom"}`)
	got, _, err := (ResponseTransform{Converter: converter}).Apply(errResp)
	if err != nil || got != errResp || converter.calls != 0 {
		t.Fatalf("non-2xx: got=%v err=%v calls=%d", got, err, converter.calls)
	}
	empty := responseWith(204, "")
	got, _, err = (ResponseTransform{Converter: converter}).Apply(empty)
	if err != nil || got != empty || converter.calls != 0 {
		t.Fatalf("empty: got=%v err=%v calls=%d", got, err, converter.calls)
	}
}

func TestResponseTransformLimitsAndPropagatesErrors(t *testing.T) {
	if _, _, err := (ResponseTransform{Converter: &fakeResponseConverter{}, MaxBytes: 4}).Apply(responseWith(200, "12345")); err == nil {
		t.Fatal("oversized body did not error")
	}
	want := errors.New("convert")
	if _, _, err := (ResponseTransform{Converter: &fakeResponseConverter{err: want}}).Apply(responseWith(200, "x")); !errors.Is(err, want) {
		t.Fatalf("err = %v", err)
	}
}
