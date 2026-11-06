package monitor

import (
	"context"
	"io"
	"os"
	"strings"
	"sync/atomic"
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
	// tracePreviewConcurrentReads caps how many cassette readers may hold a
	// preview buffer at once across every in-flight monitor response. A buffer is
	// up to tracePreviewBodyLimit (4 MiB), so this is the process-wide memory
	// bound; tracePreviewWorkers only bounds one response.
	tracePreviewConcurrentReads = 8
)

// tracePreviewSlots is the process-wide gate for tracePreviewConcurrentReads.
var tracePreviewSlots = make(chan struct{}, tracePreviewConcurrentReads)

// tracePreviewDeadlineMisses counts the previews abandoned because the budget
// ran out (or a reader slot could not be acquired before it did). A read that
// blew the deadline is counted here instead of being waited for.
var tracePreviewDeadlineMisses atomic.Int64

// tracePreviewRead reads one cassette's request preview. It is a package-level
// seam so a test can substitute a slow, failing or mixed reader; production uses
// traceRequestPreview.
var tracePreviewRead = traceRequestPreview

// attachTracePreviews fills RequestPreview from each row's cassette. Rows that
// share a cassette (a parent and its child never do, but a repeated row in one
// response might) are read once.
func attachTracePreviews(items []traceListItem) {
	attachTracePreviewsWith(items, tracePreviewRead, tracePreviewBudget)
}

// attachTracePreviewsWith is attachTracePreviews with the reader and the budget
// injected so tests can drive the deadline deterministically.
//
// The budget is a hard bound, not a "check before starting" hint: once it
// expires this function stops collecting, leaves every row it has not heard
// from without a preview and records the miss. In-flight reads are cancelled
// through their context and never waited for, so one slow cassette cannot hold
// the response open.
func attachTracePreviewsWith(items []traceListItem, read func(context.Context, string) string, budget time.Duration) {
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

	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	type previewResult struct {
		path string
		text string
	}
	// results is buffered for every path so a worker never blocks sending a
	// result after the collector has already returned on the deadline.
	results := make(chan previewResult, len(paths))
	jobs := make(chan string)
	workers := tracePreviewWorkers
	if workers > len(paths) {
		workers = len(paths)
	}
	for i := 0; i < workers; i++ {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case path, ok := <-jobs:
					if !ok {
						return
					}
					text := ""
					if acquireTracePreviewSlot(ctx) {
						text = read(ctx, path)
						releaseTracePreviewSlot()
					} else {
						tracePreviewDeadlineMisses.Add(1)
					}
					results <- previewResult{path: path, text: text}
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, path := range paths {
			select {
			case jobs <- path:
			case <-ctx.Done():
				return
			}
		}
	}()

	previews := make(map[string]string, len(paths))
	for remaining := len(paths); remaining > 0; {
		select {
		case result := <-results:
			previews[result.path] = result.text
			remaining--
		case <-ctx.Done():
			// Stop waiting: the late reads are cancelled by ctx, and the rows
			// still outstanding stay preview-less exactly like a skipped read.
			tracePreviewDeadlineMisses.Add(int64(remaining))
			remaining = 0
		}
	}

	eachTraceItem(items, func(item *traceListItem) {
		if text, ok := previews[item.logPath]; ok {
			item.RequestPreview = text
		}
	})
}

// acquireTracePreviewSlot takes one of the process-wide reader slots, or gives
// up when the preview budget expires first. The caller counts the miss in both
// cases so the two ways a read can be abandoned are not conflated.
func acquireTracePreviewSlot(ctx context.Context) bool {
	select {
	case tracePreviewSlots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func releaseTracePreviewSlot() {
	<-tracePreviewSlots
}

// traceRequestPreview reads one cassette's request body and returns the opening
// user message, trimmed for a single list line.
//
// It reads the request section only. The layout lengths are in the prelude, so
// the response body - usually the largest part of a recording - is never
// touched. A missing or unreadable cassette is not an error: the row simply has
// no preview. The context is checked between the prelude read and every chunk of
// the body so an over-budget read stops instead of finishing a 4 MiB copy.
func traceRequestPreview(ctx context.Context, path string) string {
	if ctx.Err() != nil {
		return ""
	}
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
	if err := readFullContext(ctx, file, body); err != nil {
		return ""
	}

	endpoint := firstNonEmpty(parsed.Header.Meta.Endpoint, parsed.Header.Meta.URL)
	messages := parseRequestMessagesViaAdapter(parsed.Header.Meta.Provider, endpoint, body)
	if len(messages) == 0 {
		messages = parseRequestMessages(parsed.Header.Meta.URL, body)
	}
	return firstUserMessagePreview(messages)
}

// readFullContext fills buf from r in chunks, checking ctx between them so a
// cancelled preview stops reading the body promptly.
func readFullContext(ctx context.Context, r io.Reader, buf []byte) error {
	const chunkSize = 64 << 10
	for len(buf) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk := len(buf)
		if chunk > chunkSize {
			chunk = chunkSize
		}
		if _, err := io.ReadFull(r, buf[:chunk]); err != nil {
			return err
		}
		buf = buf[chunk:]
	}
	return nil
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
