// Package modelinfo resolves channel model names against Argus, the static LLM
// model metadata registry, and projects the result into the shape the Monitor's
// model square renders.
//
// The registry is compiled into the binary, so a lookup is a map read and the
// console never calls a model catalogue over the network. Resolution is
// deliberately best-effort in one direction only: a channel model is mapped to
// a catalog record when the name or one of its channel spellings matches an
// Argus ID or alias, and no record is returned otherwise. A wrong model card is
// worse than a missing one, so the rewrite rules below only remove deployment
// decoration (a provider prefix, a version suffix, a dated snapshot) and never
// substitute a different family.
package modelinfo

import (
	"regexp"
	"strings"

	argus "github.com/kingfs/Argus"
)

// Facts is the subset of a catalog record the model square and the model detail
// page show. It is a plain struct with JSON tags because it is sent to the
// browser as-is; the Argus interface is never exposed across the API boundary.
type Facts struct {
	Query         string   `json:"query"`
	Matched       bool     `json:"matched"`
	ID            string   `json:"id,omitempty"`
	Name          string   `json:"name,omitempty"`
	Provider      string   `json:"provider,omitempty"`
	Developer     string   `json:"developer,omitempty"`
	Family        string   `json:"family,omitempty"`
	Series        string   `json:"series,omitempty"`
	Summary       string   `json:"summary,omitempty"`
	Description   string   `json:"description,omitempty"`
	DescriptionCN string   `json:"description_cn,omitempty"`
	OfficialURL   string   `json:"official_url,omitempty"`
	ModelCardURL  string   `json:"model_card_url,omitempty"`
	ContextLength int      `json:"context_length,omitempty"`
	MaxOutput     int      `json:"max_output,omitempty"`
	Capabilities  []string `json:"capabilities,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	Aliases       []string `json:"aliases,omitempty"`
}

// Lookup resolves model to a catalog record. The second result is true exactly
// when a record was found; the returned Facts still carries the query in the
// unmatched case so a caller can echo what it asked for.
func Lookup(model string) (Facts, bool) {
	query := strings.TrimSpace(model)
	facts := Facts{Query: query}
	if query == "" {
		return facts, false
	}
	for _, candidate := range candidates(query) {
		record, ok := argus.Get(candidate)
		if !ok {
			continue
		}
		facts.Matched = true
		facts.ID = record.ID()
		facts.Name = record.Name()
		facts.Provider = record.Provider()
		facts.Summary = record.Summary()
		facts.Description = record.Description()
		facts.DescriptionCN = record.DescriptionCN()
		facts.Family = record.Family()
		facts.Series = record.Series()
		facts.ContextLength = record.ContextLength()
		facts.MaxOutput = record.MaxOutput()
		facts.Capabilities = record.Features().ToStrings()
		facts.Tags = append([]string(nil), record.Tags()...)
		facts.Aliases = append([]string(nil), record.Aliases()...)
		card := record.Card()
		facts.Developer = card.Developer
		facts.OfficialURL = card.OfficialURL
		facts.ModelCardURL = card.ModelCardURL
		return facts, true
	}
	return facts, false
}

// maxCandidates bounds the breadth-first rewrite. A real model name resolves in
// the first few spellings; the cap only stops a pathological input from walking
// every combination.
const maxCandidates = 64

// dateSuffix matches the release decorations vendors append to a model id: a
// calendar date (20241022, 2024-10-22, 2024.10.22) with an optional revision
// (-v2 or -v2:0), or a bare revision.
var (
	dateSuffix     = regexp.MustCompile(`-(?:\d{8}|\d{4}[-.]\d{2}[-.]\d{2})(?:-v\d+(?:[.:]\d+)?)?$`)
	revisionSuffix = regexp.MustCompile(`-v\d+(?:[.:]\d+)?$`)
	atSuffix       = regexp.MustCompile(`@[\d.\-]+$`)
	digitDashDigit = regexp.MustCompile(`([0-9])-([0-9])`)
	lettersOnly    = regexp.MustCompile(`^[a-z]+$`)
	latestSuffix   = regexp.MustCompile(`(?i)-latest$`)
)

// candidates returns the spellings of name that are worth asking the registry
// about, most literal first. The rules are:
//
//   - the name as written and its lowercase form (registry IDs are lowercase);
//   - every trailing path segment, so a routing prefix such as azure/ or
//     openrouter/openai/ does not hide the model id;
//   - a leading dotted vendor namespace, so a Bedrock deployment id
//     (us.anthropic.claude-…) resolves to the model it deploys;
//   - a release decoration stripped from the end (a date, a revision, @date,
//     the -latest marketing suffix, a :tag such as :free or :beta);
//   - hyphens between two digits rewritten to dots, which is the spelling
//     difference between an API id (claude-3-5-sonnet) and the registry id
//     (claude-3.5-sonnet).
func candidates(name string) []string {
	queue := []string{name}
	seen := make(map[string]struct{}, 16)
	out := make([]string, 0, 16)
	for len(queue) > 0 && len(out) < maxCandidates {
		current := strings.ToLower(strings.TrimSpace(queue[0]))
		queue = queue[1:]
		if len(current) < 2 {
			continue
		}
		if _, ok := seen[current]; ok {
			continue
		}
		seen[current] = struct{}{}
		out = append(out, current)
		queue = append(queue, rewriteCandidates(current)...)
	}
	return out
}

func rewriteCandidates(name string) []string {
	var out []string
	add := func(value string) {
		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			out = append(out, value)
		}
	}
	add(strings.ToLower(name))

	// A dotted vendor namespace: us.anthropic.claude-…, anthropic.claude-….
	if index := strings.IndexByte(name, '.'); index > 0 {
		prefix := strings.ToLower(name[:index])
		if lettersOnly.MatchString(prefix) {
			add(name[index+1:])
		}
	}

	// A routing or aggregation prefix: openrouter/openai/gpt-4o, azure/gpt-4o.
	for index := 0; index < len(name); index++ {
		if name[index] == '/' && index+1 < len(name) {
			add(name[index+1:])
		}
	}

	if trimmed, ok := stripTrailing(name, dateSuffix); ok {
		add(trimmed)
	}
	if trimmed, ok := stripTrailing(name, revisionSuffix); ok {
		add(trimmed)
	}
	if trimmed, ok := stripTrailing(name, atSuffix); ok {
		add(trimmed)
	}
	if trimmed, ok := stripTrailing(name, latestSuffix); ok {
		add(trimmed)
	}
	if index := strings.LastIndexByte(name, ':'); index > 0 && index < len(name)-1 {
		// A protocol/deployment tag (:free, :beta, :thinking), not a bare
		// numeric revision such as the :0 of a Bedrock id.
		if !allDigits(name[index+1:]) {
			add(name[:index])
		}
	}
	if rewritten := digitDashDigit.ReplaceAllString(name, "$1.$2"); rewritten != name {
		add(rewritten)
	}
	return out
}

func stripTrailing(name string, pattern *regexp.Regexp) (string, bool) {
	index := pattern.FindStringIndex(name)
	if index == nil || index[0] <= 0 {
		return "", false
	}
	return name[:index[0]], true
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}
