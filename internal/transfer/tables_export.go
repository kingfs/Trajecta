package transfer

import (
	"archive/tar"
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
)

// bundleWriter wraps the tar writer of an export. Database tables are
// buffered per table and written with a known size; cassettes stream from
// disk through the same tar writer.
type bundleWriter struct {
	tw *tar.Writer
}

func (b bundleWriter) WriteEntry(name string, data []byte, modTime time.Time) error {
	hdr := &tar.Header{
		Name:     name,
		Mode:     0o600,
		Size:     int64(len(data)),
		Typeflag: tar.TypeReg,
		ModTime:  modTime,
	}
	if err := b.tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := b.tw.Write(data)
	return err
}

// runTableExport streams one table into one JSONL tar entry. Rows are buffered
// in memory per table (logs top out around ~150 MB for a quarter of a million
// rows; anything bigger is a deployment this feature does not target yet).
func runTableExport[T any](ctx context.Context, view *store.Store, table, entry string, query string, args []any, tw bundleWriter, log *Logger, scan func(*sql.Rows) (T, error), transform func(T) (T, error)) (int64, error) {
	rows, err := view.TransferQuery(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("query %s: %w", table, err)
	}
	defer rows.Close()
	lines := &strings.Builder{}
	n := int64(0)
	for rows.Next() {
		row, err := scan(rows)
		if err != nil {
			return n, err
		}
		if transform != nil {
			row, err = transform(row)
			if err != nil {
				return n, err
			}
		}
		line, err := jsonLine(row)
		if err != nil {
			return n, err
		}
		lines.Write(line)
		n++
	}
	if err := rows.Err(); err != nil {
		return n, err
	}
	body := lines.String()
	if err := tw.WriteEntry(entry, []byte(body), time.Now().UTC()); err != nil {
		return n, err
	}
	return n, nil
}

// exportTable dispatches one table to its exporter. Every exporter writes
// exactly one db/<table>.jsonl entry.
func exportTable(ctx context.Context, view *store.Store, table string, window exportWindow, tw bundleWriter, log *Logger) (int64, error) {
	entry := tableEntryName(table)
	now := time.Now().UTC()
	_ = now
	switch table {
	case "channel_configs":
		return runTableExport(ctx, view, table, entry,
			`SELECT `+channelConfigScan+` FROM channel_configs`, nil, tw, log,
			scanChannelConfig, func(row channelConfigRow) (channelConfigRow, error) {
				if len(row.apiKeyCipher) > 0 {
					plain, err := view.TransferDecryptSecretString(string(row.apiKeyCipher))
					if err != nil {
						return row, fmt.Errorf("decrypt api key for channel %q: %w", row.ID, err)
					}
					row.APIKey = plain
				}
				row.apiKeyCipher = nil
				headers, err := view.TransferDecryptHeadersJSON(row.HeadersJSON)
				if err != nil {
					return row, fmt.Errorf("decrypt headers for channel %q: %w", row.ID, err)
				}
				row.HeadersJSON = headers
				return row, nil
			})
	case "channel_models":
		return runTableExport(ctx, view, table, entry,
			`SELECT id, `+channelModelScan+` FROM channel_models`, nil, tw, log,
			scanChannelModel, nil)
	case "model_aliases":
		return runTableExport(ctx, view, table, entry,
			`SELECT `+modelAliasScan+` FROM model_aliases`, nil, tw, log,
			scanModelAlias, nil)
	case "model_catalog":
		return runTableExport(ctx, view, table, entry,
			`SELECT `+modelCatalogScan+` FROM model_catalog`, nil, tw, log,
			scanModelCatalog, nil)
	case "app_settings":
		return runTableExport(ctx, view, table, entry,
			`SELECT `+appSettingScan+` FROM app_settings`, nil, tw, log,
			scanAppSetting, nil)
	case "channel_probe_runs":
		return runTableExport(ctx, view, table, entry,
			`SELECT `+channelProbeRunScan+` FROM channel_probe_runs`, nil, tw, log,
			scanChannelProbeRun, nil)
	case "datasets":
		return runTableExport(ctx, view, table, entry,
			`SELECT `+datasetScan+` FROM datasets`, nil, tw, log,
			scanDataset, nil)
	case "eval_runs":
		return runTableExport(ctx, view, table, entry,
			`SELECT `+evalRunScan+` FROM eval_runs`, nil, tw, log,
			scanEvalRun, nil)
	case "experiment_runs":
		return runTableExport(ctx, view, table, entry,
			`SELECT `+experimentRunScan+` FROM experiment_runs`, nil, tw, log,
			scanExperimentRun, nil)
	case "dataset_examples":
		return runTableExport(ctx, view, table, entry,
			`SELECT `+datasetExampleScan+` FROM dataset_examples`, nil, tw, log,
			scanDatasetExample, nil)
	case "scores":
		return runTableExport(ctx, view, table, entry,
			`SELECT `+scoreScan+` FROM scores`, nil, tw, log,
			scanScore, nil)
	case "responses":
		return runTableExport(ctx, view, table, entry,
			`SELECT `+responseScan+` FROM responses`, nil, tw, log,
			scanResponse, nil)
	case "response_items":
		return runTableExport(ctx, view, table, entry,
			`SELECT `+responseItemScan+` FROM response_items`, nil, tw, log,
			scanResponseItem, nil)
	case "request_audits":
		return runTableExport(ctx, view, table, entry,
			`SELECT `+requestAuditScan+` FROM request_audits`, nil, tw, log,
			scanRequestAudit, nil)
	case "execution_events":
		return runTableExport(ctx, view, table, entry,
			`SELECT `+executionEventScan+` FROM execution_events`, nil, tw, log,
			scanExecutionEvent, nil)
	case "tool_call_audits":
		return runTableExport(ctx, view, table, entry,
			`SELECT `+toolCallAuditScan+` FROM tool_call_audits`, nil, tw, log,
			scanToolCallAudit, nil)
	case "system_events":
		return runTableExport(ctx, view, table, entry,
			`SELECT `+systemEventScan+` FROM system_events`, nil, tw, log,
			scanSystemEvent, nil)
	case "analysis_runs":
		return runTableExport(ctx, view, table, entry,
			`SELECT `+analysisRunScan+` FROM analysis_runs`, nil, tw, log,
			scanAnalysisRun, nil)
	case "logs":
		where, args := window.logWhere()
		return runTableExport(ctx, view, table, entry,
			`SELECT `+logScanColumns+` FROM logs `+where, args, tw, log,
			scanLog, nil)
	case "upstream_exchanges":
		return runTableExport(ctx, view, table, entry,
			`SELECT `+upstreamExchangeScan+` FROM upstream_exchanges`, nil, tw, log,
			scanUpstreamExchange, nil)
	case "credential_mask_audits":
		return runTableExport(ctx, view, table, entry,
			`SELECT `+credentialMaskAuditScan+` FROM credential_mask_audits`, nil, tw, log,
			scanCredentialMaskAudit, func(row credentialMaskAuditRow) (credentialMaskAuditRow, error) {
				original, err := view.TransferDecryptSecretString(row.OriginalValue)
				if err != nil {
					return row, fmt.Errorf("decrypt mask audit original value: %w", err)
				}
				standIn, err := view.TransferDecryptSecretString(row.StandInValue)
				if err != nil {
					return row, fmt.Errorf("decrypt mask audit stand-in value: %w", err)
				}
				row.OriginalValue = original
				row.StandInValue = standIn
				return row, nil
			})
	case "credential_mask_event_states":
		return runTableExport(ctx, view, table, entry,
			`SELECT `+credentialMaskEventStateScan+` FROM credential_mask_event_states`, nil, tw, log,
			scanCredentialMaskEventState, nil)
	default:
		return 0, fmt.Errorf("unknown export table %q", table)
	}
}

// channelModelScan lists every channel_models column after the identity
// column, which the export prepends explicitly.
const channelModelScan = `channel_id, model, display_name, source, enabled, supports_responses, supports_chat_completions, supports_embeddings, context_window, max_output_tokens, compact_history_item_threshold, upstream_model, profile_source, profile_adoption_status, input_modalities_json, output_modalities_json, raw_model_json, protocols_json, first_seen_at, last_seen_at, last_probe_at`
