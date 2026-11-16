import React, { useState } from "react";
import { Button } from "../ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "../ui/dialog";
import { InlineTag } from "../common/Badges";
import { EmptyState } from "../common/EmptyState";
import { apiPaths, postJSON } from "../../lib/api";
import { useI18n } from "../../lib/i18n";
import { useWriteMutation } from "../../lib/mutations";
import { sandboxFindingRows, sandboxStandInState } from "../../lib/detectorStats";

/**
 * The read-only secretmask rule sandbox: paste a body, run the builtin scanner
 * over it (POST /api/detector-plugins/secretmask/test) and read every finding's
 * rule, span, confidence, suppression decision and format-preserving stand-in.
 *
 * The server renders stand-ins with a throwaway in-memory vault, so a run never
 * writes the durable mapping store, the suppression list or an audit row. The
 * dialog says so explicitly: an operator pasting a real credential here must
 * know the pair will not be remembered (or restored) anywhere.
 */
export function DetectorSandboxDialog({ open, onOpenChange }) {
  const { t } = useI18n();
  const [body, setBody] = useState("");
  const [result, setResult] = useState(null);

  const run = useWriteMutation({
    mutationFn: (payload) => postJSON(apiPaths.detectorPluginSecretmaskTest, payload),
    error: "plugins.sandbox.error",
    onSuccess: (data) => setResult(data),
    onError: () => setResult(null),
  });

  const submit = (event) => {
    event.preventDefault();
    if (!body.trim() || run.isPending) return;
    setResult(null);
    run.mutate({ body });
  };

  const rows = sandboxFindingRows(result);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="detector-sandbox-modal" showClose>
        <DialogHeader>
          <div>
            <p className="eyebrow">{t("plugins.sandbox.eyebrow")}</p>
            <DialogTitle>{t("plugins.sandbox.title")}</DialogTitle>
            <DialogDescription>{t("plugins.sandbox.readOnly")}</DialogDescription>
          </div>
        </DialogHeader>
        <form className="detector-sandbox-form" onSubmit={submit}>
          <textarea
            className="detector-sandbox-input mono"
            rows={8}
            value={body}
            onChange={(event) => setBody(event.target.value)}
            placeholder={t("plugins.sandbox.bodyPlaceholder")}
            aria-label={t("plugins.sandbox.bodyLabel")}
            spellCheck={false}
          />
          <DialogFooter>
            <Button variant="ghost" type="button" onClick={() => onOpenChange(false)}>
              {t("common.close")}
            </Button>
            <Button variant="primary" type="submit" disabled={run.isPending || !body.trim()}>
              {run.isPending ? t("plugins.sandbox.running") : t("plugins.sandbox.run")}
            </Button>
          </DialogFooter>
        </form>

        {run.isError ? (
          <EmptyState
            title={t("plugins.sandbox.failed")}
            detail={run.error?.message || t("plugins.sandbox.error")}
            tone="danger"
            compact
          />
        ) : null}

        {result && !run.isError ? (
          rows.length ? (
            <>
              <p className="system-note">
                {t("plugins.sandbox.findings", { count: rows.length, length: result.body_length ?? 0 })}
              </p>
              <div className="detector-stats-table-wrap">
                <table className="detector-stats-table detector-sandbox-table">
                  <thead>
                    <tr>
                      <th>{t("plugins.sandbox.colRule")}</th>
                      <th>{t("plugins.sandbox.colSpan")}</th>
                      <th>{t("plugins.sandbox.colConfidence")}</th>
                      <th>{t("plugins.sandbox.colState")}</th>
                      <th>{t("plugins.sandbox.colStandIn")}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {rows.map((row, index) => (
                      <tr key={`${row.rule}:${row.start}:${row.end}:${index}`}>
                        <td className="mono">
                          {row.rule}
                          {row.vendor ? <span className="detector-rule-vendor"> {row.vendor}</span> : null}
                        </td>
                        <td className="mono">{row.start}–{row.end}</td>
                        <td title={row.reasons || undefined}>
                          {row.confidence || "—"}
                          <span className="detector-rule-vendor"> ({row.score})</span>
                        </td>
                        <td>
                          {row.suppressed ? (
                            <InlineTag tone="gold">{t("plugins.sandbox.suppressed")}</InlineTag>
                          ) : row.maskable ? (
                            <InlineTag tone="green">{t("plugins.sandbox.masked")}</InlineTag>
                          ) : (
                            <InlineTag>{t("plugins.detectionReportOnly")}</InlineTag>
                          )}
                        </td>
                        <td>
                          <SandboxStandIn finding={row} />
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </>
          ) : (
            <EmptyState
              title={t("plugins.sandbox.noHits")}
              detail={t("plugins.sandbox.noHitsDetail", { length: result.body_length ?? 0 })}
              compact
            />
          )
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function SandboxStandIn({ finding }) {
  const { t } = useI18n();
  const state = sandboxStandInState(finding);
  if (state === "stand_in") {
    return <code className="credential-mask-code detector-sandbox-standin">{finding.standIn}</code>;
  }
  if (state === "suppressed") {
    return <span className="trace-subline">{t("plugins.sandbox.suppressedHint")}</span>;
  }
  if (state === "report_only") {
    return <span className="trace-subline">{t("plugins.sandbox.reportOnlyHint")}</span>;
  }
  return <span className="trace-subline">—</span>;
}
