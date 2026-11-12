// Detection-plugin observability and sandbox endpoints.
//
// GET  /api/detector-plugins/stats            hot-path counters + per-rule
//                                             replacement counts + suppression
//                                             and mapping sizes.
// POST /api/detector-plugins/secretmask/test  run the builtin scanner over an
//                                             operator-supplied body and show
//                                             the rule, span, confidence,
//                                             suppression decision and stand-in.
//
// Both are admin-only and read-only: the sandbox renders stand-ins with a
// throwaway in-memory vault, so it never touches the durable mapping store.

package monitor

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/kingfs/Trajecta/internal/detector"
	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/secretmask"
)

const (
	// sandboxBodyLimit bounds the sandbox input. It is deliberately smaller
	// than the proxy's request bound: this is an interactive tool, not a
	// recording path.
	sandboxBodyLimit = 1 << 20
	// ruleStatsWindow is the window the rule tally covers.
	ruleStatsWindow = 30 * 24 * time.Hour
)

type detectorRuleStatView struct {
	Rule     string    `json:"rule"`
	Kind     string    `json:"kind"`
	Hits     int       `json:"hits"`
	LastSeen time.Time `json:"last_seen"`
}

type detectorStatsResponse struct {
	Hot          detector.StatsSnapshot `json:"hot"`
	Rules        []detectorRuleStatView `json:"rules"`
	Suppressions int                    `json:"suppressions"`
	Mappings     int                    `json:"mappings"`
}

// detectorPluginStatsAPIHandler serves GET /api/detector-plugins/stats.
func detectorPluginStatsAPIHandler(st *store.Store, rt *detector.Runtime, mappings SecretMappingAdmin, suppressions SecretSuppressionAdmin) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		resp := detectorStatsResponse{Hot: rt.Stats()}
		if st != nil {
			stats, err := st.RuleMaskStats(time.Now().Add(-ruleStatsWindow), 200)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			resp.Rules = make([]detectorRuleStatView, 0, len(stats))
			for _, stat := range stats {
				resp.Rules = append(resp.Rules, detectorRuleStatView{
					Rule: stat.Rule, Kind: stat.Kind, Hits: stat.Hits, LastSeen: stat.LastSeen,
				})
			}
		}
		resp.Suppressions = countSuppressions(suppressions)
		resp.Mappings = mappingCount(context.Background(), mappings)
		writeJSON(w, http.StatusOK, resp)
	}
}

// countSuppressions returns the operator false-positive list size. A nil or
// unavailable store reports zero, like the mapping badge.
func countSuppressions(admin SecretSuppressionAdmin) int {
	if admin == nil {
		return 0
	}
	count := 0
	err := admin.ListSuppressions(false, func(secretmask.Suppression) error {
		count++
		return nil
	})
	if err != nil {
		return 0
	}
	return count
}

type detectorTestRequest struct {
	Body string `json:"body"`
}

type detectorTestFinding struct {
	Rule       string `json:"rule"`
	Vendor     string `json:"vendor,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Start      int    `json:"start"`
	End        int    `json:"end"`
	Score      int    `json:"score"`
	Confidence string `json:"confidence"`
	Reasons    string `json:"reasons"`
	Maskable   bool   `json:"maskable"`
	Suppressed bool   `json:"suppressed"`
	StandIn    string `json:"stand_in,omitempty"`
}

type detectorTestResponse struct {
	BodyLength int                   `json:"body_length"`
	Findings   []detectorTestFinding `json:"findings"`
}

// detectorSandboxAPIHandler serves POST /api/detector-plugins/secretmask/test.
func detectorSandboxAPIHandler(suppressions SecretSuppressionAdmin) http.HandlerFunc {
	checker := suppressionCheckerFrom(suppressions)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		var req detectorTestRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, sandboxBodyLimit+1024))
		if err := decoder.Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid sandbox payload"})
			return
		}
		if len(req.Body) > sandboxBodyLimit {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "sandbox body too large"})
			return
		}
		body := []byte(req.Body)
		// A throwaway vault keeps the sandbox side-effect free: it renders
		// format-preserving stand-ins but never writes the durable mapping.
		vault := secretmask.NewVault(nil)
		resp := detectorTestResponse{BodyLength: len(body)}
		for _, finding := range (secretmask.Detector{}).Scan(body) {
			rule, ok := finding.Rule()
			if !ok {
				continue
			}
			value := finding.Bytes(body)
			if value == nil {
				continue
			}
			item := detectorTestFinding{
				Rule: rule.Name, Vendor: rule.Vendor, Kind: rule.Kind.String(),
				Start: finding.Start, End: finding.End, Score: finding.Score,
				Confidence: finding.Confidence.String(), Reasons: finding.Reasons.String(),
				Maskable: finding.Maskable(),
			}
			if checker != nil {
				item.Suppressed = checker(rule.Name, value)
			}
			if item.Maskable && !item.Suppressed {
				_, events := vault.MaskCandidates(value, []secretmask.MaskCandidate{{
					Finding: secretmask.Finding{
						Span:       secretmask.Span{Start: 0, End: len(value), ID: finding.ID},
						Score:      finding.Score,
						Confidence: finding.Confidence,
						Reasons:    finding.Reasons,
					},
				}})
				if len(events) > 0 {
					item.StandIn = string(events[0].StandIn)
				}
			}
			resp.Findings = append(resp.Findings, item)
		}
		writeJSON(w, http.StatusOK, resp)
	}
}
