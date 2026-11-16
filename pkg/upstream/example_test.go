package upstream_test

import (
	"fmt"

	"github.com/kingfs/Trajecta/pkg/upstream"
	"github.com/kingfs/Trajecta/pkg/xir"
)

func ExampleResolved_BuildURL() {
	preset, ok := upstream.LookupPreset("openai")
	if !ok {
		panic("openai preset missing")
	}
	resolved := upstream.Resolved{Preset: preset, Endpoints: preset.Endpoints, Auth: preset.Auth}
	target, err := resolved.BuildURL(xir.ProtocolChatCompletions, "/v1/chat/completions")
	if err != nil {
		panic(err)
	}
	fmt.Println(target)
	// Output: https://api.openai.com/v1/chat/completions
}
