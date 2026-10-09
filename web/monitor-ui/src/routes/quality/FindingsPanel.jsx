import React from "react";
import { Link, useSearchParams } from "react-router-dom";
import { EmptyState } from "../../components/common/EmptyState";
import { DetailMetaPill, InlineTag } from "../../components/common/Badges";
import { useJSON } from "../../hooks/useJSON";
import { apiPaths, apiURL } from "../../lib/api";
import { useI18n } from "../../lib/i18n";
import { setOrDeleteParam } from "../../lib/monitor";

export function FindingsPanel() {
  const { t } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const category = searchParams.get("category") || "";
  const severity = searchParams.get("severity") || "";
  const params = new URLSearchParams({ limit: "50" });
  if (category) {
    params.set("category", category);
  }
  if (severity) {
    params.set("severity", severity);
  }
  const findings = useJSON(apiURL(apiPaths.findings, params), [category, severity]);
  const items = findings.data?.items || [];

  const setFilter = (key, value) => {
    const next = new URLSearchParams(searchParams);
    setOrDeleteParam(next, key, value);
    setSearchParams(next);
  };
  const resetFilters = () => {
    const next = new URLSearchParams(searchParams);
    next.delete("category");
    next.delete("severity");
    setSearchParams(next);
  };

  return (
    <>
      <section className="panel">
        <div className="panel-head">
          <div>
            <h2>{t("audit.crossTraceFindings")}</h2>
          </div>
          <InlineTag tone={items.length ? "danger" : "green"}>{t("audit.totalFindings", { count: findings.data?.total ?? 0 })}</InlineTag>
        </div>
        <form className="filter-bar" onSubmit={(event) => event.preventDefault()}>
          <input className="filter-input" type="search" placeholder={t("audit.category")} value={category} onChange={(event) => setFilter("category", event.target.value)} />
          <select className="filter-input" aria-label={t("common.severity")} value={severity} onChange={(event) => setFilter("severity", event.target.value)}>
            <option value="">{t("audit.anySeverity")}</option>
            <option value="critical">{t("audit.severityCritical")}</option>
            <option value="high">{t("audit.severityHigh")}</option>
            <option value="medium">{t("audit.severityMedium")}</option>
            <option value="low">{t("audit.severityLow")}</option>
          </select>
          <button className="ghost-button" type="button" onClick={resetFilters}>{t("common.reset")}</button>
        </form>
        {findings.error ? <EmptyState title={t("audit.loadFindingsError")} detail={findings.error} tone="danger" /> : null}
        {findings.loading && !findings.data ? <EmptyState title={t("audit.loadingFindings")} detail={t("audit.loadingFindingsDetail")} /> : null}
        {items.length ? (
          <div className="finding-list">
            {items.map((finding) => (
              <article key={`${finding.trace_id}-${finding.id}`} className="finding-card">
                <div className="finding-card-head">
                  <div>
                    <strong>{finding.title || finding.category}</strong>
                    <span>{finding.description || finding.evidence_path}</span>
                  </div>
                  <div className="trace-tag-group">
                    <InlineTag tone={finding.severity === "high" || finding.severity === "critical" ? "danger" : "gold"}>{finding.severity}</InlineTag>
                    <InlineTag>{finding.category}</InlineTag>
                  </div>
                </div>
                <div className="detail-meta-strip">
                  <DetailMetaPill label={t("common.trace")} value={finding.trace_id} mono />
                  <DetailMetaPill label={t("common.node")} value={finding.node_id || "-"} mono />
                  <DetailMetaPill label={t("common.detector")} value={`${finding.detector || "-"} ${finding.detector_version || ""}`.trim()} />
                </div>
                <div className="action-group action-group-start">
                  <Link className="ghost-button" to={`/traces/${encodeURIComponent(finding.trace_id)}?tab=audit`}>{t("audit.openFinding")}</Link>
                  <Link className="ghost-button" to={`/traces/${encodeURIComponent(finding.trace_id)}?tab=protocol`}>{t("audit.protocol")}</Link>
                </div>
              </article>
            ))}
          </div>
        ) : findings.data ? (
          <EmptyState title={t("audit.noFindings")} detail={t("audit.noFindingsDetail")} />
        ) : null}
      </section>
    </>
  );
}
