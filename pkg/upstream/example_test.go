package upstream_test

import (
	"fmt"

	"github.com/kingfs/Trajecta/pkg/upstream"
	"github.com/kingfs/Trajecta/pkg/xir"
)

func ExampleResolve() {
	resolved, err := upstream.Resolve(upstream.Config{PresetID: "deepseek"})
	if err != nil {
		panic(err)
	}
	fmt.Println(resolved.Endpoints[xir.ProtocolChatCompletions])
	fmt.Println(resolved.Endpoints[xir.ProtocolMessages])
	fmt.Println(resolved.Auth.Mode)
	// Output:
	// https://api.deepseek.com/v1
	// https://api.deepseek.com/anthropic
	// api_key
}

func ExampleResolved_BuildURL() {
	resolved, err := upstream.Resolve(upstream.Config{PresetID: "openai"})
	if err != nil {
		panic(err)
	}
	target, err := resolved.BuildURL(xir.ProtocolChatCompletions, "/v1/chat/completions")
	if err != nil {
		panic(err)
	}
	fmt.Println(target)
	// Output: https://api.openai.com/v1/chat/completions
}

func ExampleResolved_Capabilities() {
	resolved, err := upstream.Resolve(upstream.Config{PresetID: "deepseek"})
	if err != nil {
		panic(err)
	}
	capabilities := resolved.Capabilities("deepseek-chat")
	fmt.Println(capabilities.ChatCompletions, capabilities.Responses, capabilities.Messages, capabilities.Gemini)
	// Output: true true true false
}
