package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestProductionScannersSetTheirOwnBound keeps bufio.Scanner's 64 KiB default out of the codebase.
//
// bufio.Scanner stops at a line longer than its buffer and every one of these call sites ignored the
// error, so too small a bound showed up as silently dropped data rather than a failure: a 2 MiB SSE
// frame was recorded and then lost in the readers (round 23), and an SSE-wrapped JSON-RPC message
// above 64 KiB was handed to json.Unmarshal as the raw envelope (round 24). Each site now names a
// bound derived from the input it already holds; this gate fails when a new scanner appears without
// one, so the fix has to be repeated deliberately rather than forgotten.
func TestProductionScannersSetTheirOwnBound(t *testing.T) {
	const lookahead = 8
	roots := []string{"../../cmd", "../../internal", "../../pkg"}
	scanners := 0
	var missing []string

	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == "testdata" || entry.Name() == "node_modules" {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			lines := strings.Split(string(body), "\n")
			for idx, line := range lines {
				if !strings.Contains(line, "bufio.NewScanner(") {
					continue
				}
				scanners++
				bounded := false
				for offset := idx; offset < len(lines) && offset < idx+lookahead; offset++ {
					if strings.Contains(lines[offset], ".Buffer(") {
						bounded = true
						break
					}
				}
				if !bounded {
					missing = append(missing, path+":"+strconv.Itoa(idx+1))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}

	if scanners == 0 {
		t.Fatalf("found no bufio.NewScanner call sites under cmd, internal or pkg: this gate would pass vacuously")
	}
	if len(missing) > 0 {
		t.Fatalf("bufio.NewScanner without an explicit Buffer bound within %d lines: %s\n"+
			"bufio.Scanner stops at 64 KiB and the call sites ignore scanner.Err(), so a too small bound drops data silently. "+
			"Pass a bound derived from the input you already hold (see recordfile.MaxStreamLineBytes for SSE streams).",
			lookahead, strings.Join(missing, ", "))
	}
}
