package monitor

import (
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

const (
	// tracePreviewRunes is how much of the user's opening message a list row
	// shows. It is measured in runes because the content is usually Chinese and
	// a byte cut would break the last character.
	tracePreviewRunes = 80
	// tracePreviewBodyLimit bounds how much of one request body the list reads.
	// A request larger than this is skipped rather than truncated: a cut JSON
	// body does not parse, so there would be nothing to show anyway.
	tracePreviewBodyLimit = 4 << 20
	// tracePreviewMaxItems bounds the work one response can cause when a session
	// has hundreds of traces. The list view shows fifty rows; the cap only
	// matters for the endpoints that return a whole session.
	tracePreviewMaxItems = 200
	// A preview is one random cassette read. On the production trace volume a
	// 200-row page spent 19 s doing those reads sequentially; eight readers and
	// a short budget keep the page responsive while still filling the rows a
	// reader actually sees. A row whose read misses the budget simply has no
	// preview.
	tracePreviewWorkers = 8
	tracePreviewBudget  = 2 * time.Second
)

// attachTracePreviews fills RequestPreview from each row's cassette. Rows that
// share a cassette (a parent and its child never do, but a repeated row in one
// response might) are read once.
func attachTracePreviews(items []traceListItem) {
	previews := map[string]string{}
	paths := make([]string, 0, 16)
	seen := make(map[string]struct{}, 16)
	eachTraceItem(items, func(item *traceListItem) {
		if item.logPath == "" {
			return
		}
		if _, ok := seen[item.logPath]; ok {
			return
		}
		if len(paths) >= tracePreviewMaxItems {
			return
		}
		seen[item.logPath] = struct{}{}
		paths = append(paths, item.logPath)
	})
	if len(paths) == 0 {
		return
	}

	deadline := time.Now().Add(tracePreviewBudget)
	jobs := make(chan string)
	results := make(chan struct {
		path string
		text string
	}, len(paths))
	workers := tracePreviewWorkers
	if workers > len(paths) {
		workers = len(paths)
	}
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range jobs {
				text := ""
				if time.Now().Before(deadline) {
					text = traceRequestPreview(path)
				}
				results <- struct {
					path string
					text string
				}{path: path, text: text}
			}
		}()
	}
	go func() {
		for _, path := range paths {
			jobs <- path
		}
		close(jobs)
	}()
	wg.Wait()
	close(results)
	for result := range results {
		previews[result.path] = result.text
	}

	eachTraceItem(items, func(item *traceListItem) {
		if text, ok := previews[item.logPath]; ok {
			item.RequestPreview = text
		}
	})
}

// traceRequestPreview reads one cassette's request body and returns the opening
// user message, trimmed for a single list line.
//
// It reads the request section only. The layout lengths are in the prelude, so
// the response body - usually the largest part of a recording - is never
// touched. A missing or unreadable cassette is not an error: the row simply has
// no preview.
func traceRequestPreview(path string) string {
	parsed, err := recordfile.ReadPreludeFile(path)
	if err != nil {
		return ""
	}
	layout := parsed.Header.Layout
	if layout.ReqBodyLen <= 0 || layout.ReqBodyLen > tracePreviewBodyLimit {
		return ""
	}

	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	if _, err := file.Seek(parsed.PayloadOffset+layout.ReqHeaderLen, io.SeekStart); err != nil {
		return ""
	}
	body := make([]byte, layout.ReqBodyLen)
	if _, err := io.ReadFull(file, body); err != nil {
		return ""
	}

	endpoint := firstNonEmpty(parsed.Header.Meta.Endpoint, parsed.Header.Meta.URL)
	messages := parseRequestMessagesViaAdapter(parsed.Header.Meta.Provider, endpoint, body)
	if len(messages) == 0 {
		messages = parseRequestMessages(parsed.Header.Meta.URL, body)
	}
	return firstUserMessagePreview(messages)
}

// firstUserMessagePreview picks the first user message with text. System
// prompts are skipped on purpose: every trace of one agent has the same one, so
// it tells the operator nothing about what the user asked.
func firstUserMessagePreview(messages []ChatMessage) string {
	for _, message := range messages {
		if !strings.EqualFold(strings.TrimSpace(message.Role), "user") {
			continue
		}
		text := strings.TrimSpace(message.Content)
		if text == "" {
			parts := make([]string, 0, len(message.Blocks))
			for _, block := range message.Blocks {
				if blockText := strings.TrimSpace(block.Text); blockText != "" {
					parts = append(parts, blockText)
				}
			}
			text = strings.Join(parts, " ")
		}
		// A message can be multi-line; a row preview is one line and collapsing
		// the whitespace is what keeps the first characters the visible ones.
		if text = strings.Join(strings.Fields(text), " "); text == "" {
			continue
		}
		return truncatePreviewRunes(text, tracePreviewRunes)
	}
	return ""
}

func truncatePreviewRunes(text string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit]) + "…"
}
