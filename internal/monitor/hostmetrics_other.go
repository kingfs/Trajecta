//go:build !linux

package monitor

import "time"

// readSystemHostMetrics answers with the structured unsupported payload on
// every platform whose resource counters this package does not read. The page
// renders the same document everywhere, so it gets an empty but well-formed
// response instead of a failed request or a missing section.
func readSystemHostMetrics(now time.Time, prev *systemHostSample) (systemHostResponse, systemHostSample) {
	return systemHostResponse{
		GeneratedAt: now.UTC(),
		Unsupported: true,
		Reason:      "host resource metrics are read from the Linux /proc filesystem, which this build does not have",
		Disk:        []systemHostDisk{},
		Network:     systemHostNetwork{Interfaces: []systemHostInterface{}},
		Warnings:    []string{},
	}, systemHostSample{}
}
