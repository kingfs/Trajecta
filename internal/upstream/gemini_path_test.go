package upstream

import "testing"

func TestBuildURLFoldsGeminiEndpointVersion(t *testing.T) {
	u := ResolvedUpstream{
		BaseURL:        "https://generativelanguage.googleapis.com/v1beta",
		ProtocolFamily: ProtocolFamilyGoogleGenAI,
		RoutingProfile: RoutingProfileGoogleAIStudio,
	}
	got, err := u.BuildURL("/v1beta/models/gemini-2.5-flash:generateContent")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent"
	if got != want {
		t.Fatalf("BuildURL() = %q, want %q", got, want)
	}
}

func TestBuildURLGeminiOriginKeepsClientVersion(t *testing.T) {
	u := ResolvedUpstream{
		BaseURL:        "https://generativelanguage.googleapis.com",
		ProtocolFamily: ProtocolFamilyGoogleGenAI,
		RoutingProfile: RoutingProfileGoogleAIStudio,
	}
	got, err := u.BuildURL("/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse"
	if got != want {
		t.Fatalf("BuildURL() = %q, want %q", got, want)
	}
}

func TestBuildURLVertexMapsAIPathToResourceRoot(t *testing.T) {
	u := ResolvedUpstream{
		BaseURL:        "https://aiplatform.googleapis.com",
		ProtocolFamily: ProtocolFamilyVertexNative,
		RoutingProfile: RoutingProfileVertexExpress,
		ModelResource:  "publishers/google/models",
	}
	got, err := u.BuildURL("/v1beta/models/gemini-2.5-pro:generateContent")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://aiplatform.googleapis.com/v1/publishers/google/models/gemini-2.5-pro:generateContent"
	if got != want {
		t.Fatalf("BuildURL() = %q, want %q", got, want)
	}
}
