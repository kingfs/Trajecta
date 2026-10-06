package unittest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// TestReplayCassettesArePinned makes the two go-openai cassettes a hard
// requirement. They are the only place where the V2 (LLM_PROXY_V2, fixed 2KB
// JSON header) read path is exercised through a real SDK, so deleting or
// migrating them must fail the suite instead of quietly removing coverage.
func TestReplayCassettesArePinned(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"non-stream.http", "stream.http"} {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join("testdata", name)
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile(%s) error = %v; the go-openai replay coverage requires this legacy V2 cassette", path, err)
			}
			parsed, err := recordfile.ParsePrelude(content)
			if err != nil {
				t.Fatalf("ParsePrelude(%s) error = %v", path, err)
			}
			if parsed.Header.Version != "LLM_PROXY_V2" {
				t.Fatalf("%s version = %q, want LLM_PROXY_V2; moving it to V3 would drop the V2 SDK replay coverage", path, parsed.Header.Version)
			}
			reqFull, reqBody, resFull, _ := recordfile.ExtractSections(content, parsed)
			if len(reqFull) == 0 || len(resFull) == 0 {
				t.Fatalf("%s sections: request=%d response=%d, want both non-empty", path, len(reqFull), len(resFull))
			}
			if len(reqBody) == 0 {
				t.Fatalf("%s recorded an empty request body; the replay fixture would prove nothing", path)
			}
		})
	}
}
