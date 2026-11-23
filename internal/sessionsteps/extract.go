package sessionsteps

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/observe"
	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// facts is the per-cassette extraction the grouping and the ledger need. It is
// deliberately small: previews are truncated here, so a long session never
// holds every body in memory at once.
type facts struct {
	// parsed is false when the cassette could not be read or had no parser;
	// the step then keeps its index numbers and reports no content.
	parsed bool
	// readFailed distinguishes "we did not get to it" from "we read it and the
	// payload was unusable".
	readFailed bool

	usage observe.ObservationUsage

	messages []messageSig
	// lastUserText is the newest user message the client sent, clipped. Only
	// the last one is kept: the rest of the history is re-sent on every
	// request, and a trigger is by definition the message a turn opened with.
	lastUserText string
	// hasToolResult is true when the request tail carries tool results, which
	// is what makes a step a continuation of the previous one.
	hasToolResult bool

	toolCalls   []ToolCallView
	toolResults []toolResultFact
	tools       []ToolCount

	answer       string
	answerChars  int
	requestChars int
}

// messageSig is one conversation message reduced to what alignment needs: its
// role and a hash of its text. The text itself is not kept because the same
// history is re-sent on every request and a 500-step session would otherwise
// hold it once per step.
type messageSig struct {
	Role string
	Hash [16]byte
}

// toolResultFact is one tool result plus the position of the message that
// carried it. The position is what lets the build keep only the results this
// step's request added: an agent re-sends its whole conversation on every
// request, so an untrimmed step would carry every tool result of the session
// and one window would weigh megabytes.
type toolResultFact struct {
	view         ToolResultView
	messageIndex int
}

// readFacts parses one cassette into facts. The observation comes from the
// same parser set the monitor already uses for the trace views, so the step
// model never disagrees with what an opened trace shows.
func readFacts(ctx context.Context, registry *observe.Registry, entry store.LogEntry) facts {
	var out facts
	content, err := os.ReadFile(entry.LogPath)
	if err != nil {
		out.readFailed = true
		return out
	}
	parsed, err := recordfile.ParsePrelude(content)
	if err != nil {
		out.readFailed = true
		return out
	}
	_, reqBody, _, resBody := recordfile.ExtractSections(content, parsed)
	input := observe.ParseInput{
		TraceID:      entry.ID,
		CassettePath: entry.LogPath,
		// The database row carries the complete header even when the cassette
		// was written in minimal mode, which clears timing and usage.
		Header:           entry.Header,
		RequestBody:      reqBody,
		ResponseBody:     resBody,
		IsStream:         entry.Header.Layout.IsStream,
		ExchangeKind:     entry.Header.Meta.ExchangeKind,
		ExchangeRole:     entry.Header.Meta.ExchangeRole,
		ParentExchangeID: entry.Header.Meta.ParentExchangeID,
		SequenceIndex:    entry.Header.Meta.SequenceIndex,
		RequestAuditID:   entry.Header.Meta.RequestAuditID,
		ResponseID:       entry.Header.Meta.ResponseID,
	}
	obs, err := registry.Parse(ctx, input)
	if err != nil {
		if errors.Is(err, observe.ErrNoParser) {
			obs = observe.UnsupportedObservation(input)
		} else {
			out.readFailed = true
			return out
		}
	}
	out.parsed = true
	out.usage = obs.Usage
	out.requestChars = len(reqBody)
	out.messages, out.hasToolResult, out.toolResults, out.lastUserText = readMessages(obs)
	out.toolCalls, out.tools = readToolCalls(obs)
	out.answer, out.answerChars = readAnswer(obs)
	return out
}

// readMessages flattens the conversation the client sent and collects the tool
// results it carries. Responses puts the conversation under inputs; the other
// three protocols put it under messages.
func readMessages(obs observe.TraceObservation) ([]messageSig, bool, []toolResultFact, string) {
	nodes := obs.Request.Messages
	if len(nodes) == 0 {
		nodes = obs.Request.Inputs
	}
	sigs := make([]messageSig, 0, len(nodes))
	var results []toolResultFact
	hasToolResult := false
	lastUserText := ""
	for messageIndex, node := range nodes {
		text := node.Text
		if text == "" {
			text = joinChildText(node.Children)
		}
		role := strings.ToLower(node.Role)
		sigs = append(sigs, messageSig{Role: role, Hash: hashText(node.Role + "\x00" + text)})
		if role == "user" {
			lastUserText = text
		}
		if node.NormalizedType == observe.NodeToolResult {
			hasToolResult = true
			results = append(results, toolResultFact{view: toolResultView(node), messageIndex: messageIndex})
		}
		for _, child := range node.Children {
			if child.NormalizedType != observe.NodeToolResult {
				continue
			}
			hasToolResult = true
			results = append(results, toolResultFact{view: toolResultView(child), messageIndex: messageIndex})
		}
	}
	return sigs, hasToolResult, results, clip(lastUserText, maxTriggerRunes)
}

// toolResultView renders one client tool result. The call id is what pairs it
// with the previous step's request; each protocol spells it differently.
func toolResultView(node observe.SemanticNode) ToolResultView {
	return ToolResultView{
		CallID:  firstMetadata(node, "call_id", "tool_use_id", "tool_call_id"),
		Name:    firstMetadata(node, "name"),
		Text:    clip(node.Text, maxPreviewRunes),
		Chars:   len(node.Text),
		IsError: observe.ToolResultFailed(node),
	}
}

// readToolCalls collects the model's tool calls and groups them by name, which
// is the "执行内容" column's content.
func readToolCalls(obs observe.TraceObservation) ([]ToolCallView, []ToolCount) {
	calls := obs.Tools.Calls
	views := make([]ToolCallView, 0, len(calls))
	counts := make([]ToolCount, 0, 4)
	index := make(map[string]int, 4)
	for _, call := range calls {
		name := call.Name
		if name == "" {
			name = call.Kind
		}
		views = append(views, ToolCallView{
			ID:       call.ID,
			Name:     name,
			Args:     clip(call.ArgsText, maxPreviewRunes),
			ArgsChar: len(call.ArgsText),
		})
		if at, ok := index[name]; ok {
			counts[at].Calls++
			continue
		}
		index[name] = len(counts)
		counts = append(counts, ToolCount{Name: name, Calls: 1})
	}
	return views, counts
}

// readAnswer returns the model's text output. A streamed exchange only has the
// accumulated text; a non-streamed one carries it on the response message node,
// which sits under a choice/candidate wrapper that itself holds no text.
func readAnswer(obs observe.TraceObservation) (string, int) {
	if text := strings.TrimSpace(obs.Stream.AccumulatedText); text != "" {
		return clip(text, maxAnswerPreviewRunes), len(text)
	}
	var builder strings.Builder
	appendResponseText(obs.Response.Nodes, &builder)
	text := builder.String()
	return clip(text, maxAnswerPreviewRunes), len(text)
}

// appendResponseText walks the response tree but only accepts message and text
// nodes, so a tool call's name is never mistaken for what the model said. A
// node that carries text ends the descent: its text already covers the text
// children it was built from.
func appendResponseText(nodes []observe.SemanticNode, builder *strings.Builder) {
	for _, node := range nodes {
		if node.NormalizedType == observe.NodeMessage || node.NormalizedType == observe.NodeText {
			if node.Text != "" {
				builder.WriteString(node.Text)
				continue
			}
		}
		appendResponseText(node.Children, builder)
	}
}

func joinChildText(nodes []observe.SemanticNode) string {
	var builder strings.Builder
	for _, node := range nodes {
		if node.Text != "" {
			builder.WriteString(node.Text)
		}
		builder.WriteString(joinChildText(node.Children))
	}
	return builder.String()
}

func firstMetadata(node observe.SemanticNode, keys ...string) string {
	for _, key := range keys {
		if node.Metadata == nil {
			return ""
		}
		switch value := node.Metadata[key].(type) {
		case string:
			if strings.TrimSpace(value) != "" {
				return value
			}
		case json.Number:
			return value.String()
		}
	}
	return ""
}

func hashText(value string) [16]byte {
	sum := sha256.Sum256([]byte(value))
	var out [16]byte
	copy(out[:], sum[:16])
	return out
}

// clip truncates by rune so a multi-byte character is never split.
func clip(value string, maxRunes int) string {
	if maxRunes <= 0 || utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	runes := []rune(value)
	return string(runes[:maxRunes]) + "…"
}
