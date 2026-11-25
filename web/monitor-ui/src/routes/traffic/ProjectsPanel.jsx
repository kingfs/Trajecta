import React, { useEffect, useMemo, useState } from "react";
import { Card } from "../../components/ui/card";
import { Input } from "../../components/ui/input";
import { Button } from "../../components/ui/button";
import { useSearchParams } from "react-router-dom";
import { StatCard } from "../../components/common/Display";
import { EmptyState } from "../../components/common/EmptyState";
import { Pager } from "../../components/common/Pager";
import { InlineTag } from "../../components/common/Badges";
import { Dialog, DialogClose, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "../../components/ui/dialog";
import { useJSON } from "../../hooks/useJSON";
import { useRefresh } from "../../hooks/useRefresh";
import { apiPaths, apiURL, postJSON, requestJSON } from "../../lib/api";
import { useI18n } from "../../lib/i18n";
import { canOpenSystemPage } from "../../lib/nav";
import { formatDateTime, formatRawNumber, formatTokenCount, normalizeListWindow, setOrDeleteParam } from "../../lib/monitor";
import { toast } from "sonner";

const PAGE_SIZE = 50;

// The project list answers "how many sessions do we have per project".
//
// A project is the directory a client harness declared about itself in the
// request prompt, not a path a model touched, so a session with no declaration
// is in no project at all. Two controls make that honest over time:
//
// - 识别健康 counts the declaration blocks that produced no usable project,
//   which is how a harness changing its prompt format becomes visible instead
//   of silently lowering the attribution rate;
// - 合并 lets an operator declare that two paths are the same project (a
//   container mount and the host checkout, a worktree and its repository).
//   A merge is applied when reading, so it rewrites no history and deleting
//   the alias undoes it.
export function ProjectsPanel() {
  const { t } = useI18n();
  const refresh = useRefresh();
  const [searchParams, setSearchParams] = useSearchParams();
  const page = Math.max(1, Number.parseInt(searchParams.get("page") || "1", 10) || 1);
  const query = searchParams.get("q") || "";
  const windowValue = normalizeListWindow(searchParams.get("window"));
  const [filters, setFilters] = useState({ query });
  const [mergeSource, setMergeSource] = useState(null);
  const [mergeTarget, setMergeTarget] = useState("");
  const [busy, setBusy] = useState(false);

  const requestParams = new URLSearchParams({
    page: String(page),
    page_size: String(PAGE_SIZE),
  });
  if (query) {
    requestParams.set("q", query);
  }
  requestParams.set("window", windowValue);
  const { loading, data, error } = useJSON(apiURL(apiPaths.projects, requestParams), [page, query, windowValue]);
  const detection = useJSON(apiURL(apiPaths.projectsDetection, { window: windowValue }), [windowValue]);
  const aliases = useJSON(apiPaths.projectAliases);
  const me = useJSON(apiPaths.authMe);
  const canAdmin = canOpenSystemPage(me.data);
  // Merge targets come from the unfiltered project list, so a merge can point
  // at a project that the current search or window does not show.
  const targets = useJSON(
    canAdmin ? apiURL(apiPaths.projects, { window: "all", page: "1", page_size: "200" }) : null,
    [canAdmin],
  );

  useEffect(() => {
    setFilters({ query });
  }, [query]);

  const items = data?.items ?? [];
  const identified = data?.identified_sessions ?? 0;
  const totalSessions = data?.total_sessions ?? 0;
  const coverage = totalSessions > 0 ? (100 * identified) / totalSessions : 0;
  const detectionData = detection.data ?? {};
  const rejected = detectionData.rejected ?? 0;
  const aliasItems = aliases.data?.items ?? [];
  const targetItems = useMemo(
    () => (targets.data?.items ?? []).filter((item) => item.root_key !== mergeSource?.root_key),
    [targets.data, mergeSource],
  );

  const goToPage = (nextPage) => {
    const next = new URLSearchParams(searchParams);
    next.set("page", String(nextPage));
    setSearchParams(next);
  };
  const applyFilters = (event) => {
    event.preventDefault();
    const next = new URLSearchParams(searchParams);
    next.set("page", "1");
    setOrDeleteParam(next, "q", filters.query);
    setSearchParams(next);
  };
  const resetFilters = () => {
    setFilters({ query: "" });
    const next = new URLSearchParams(searchParams);
    next.set("page", "1");
    next.delete("q");
    setSearchParams(next);
  };
  const openProject = (rootKey) => {
    const next = new URLSearchParams(searchParams);
    next.set("tab", "sessions");
    next.set("project", rootKey);
    next.delete("page");
    setSearchParams(next);
  };

  const openMerge = (item) => {
    setMergeSource(item);
    setMergeTarget("");
  };
  const closeMerge = () => {
    if (busy) {
      return;
    }
    setMergeSource(null);
    setMergeTarget("");
  };
  const submitMerge = async (event) => {
    event.preventDefault();
    const target = targetItems.find((item) => item.root_key === mergeTarget);
    if (!target || !mergeSource) {
      return;
    }
    setBusy(true);
    try {
      await postJSON(apiPaths.projectAliases, {
        alias_key: mergeSource.root_key,
        canonical_key: target.root_key,
        canonical_root: target.root,
        note: "",
      });
      toast.success(t("projects.mergeSuccess"));
      closeMerge();
      refresh();
    } catch (err) {
      toast.error(err?.payload?.error || err?.message || t("common.actionFailed"));
    } finally {
      setBusy(false);
    }
  };
  const deleteAlias = async (aliasKey) => {
    setBusy(true);
    try {
      await requestJSON(apiURL(apiPaths.projectAliases, { alias_key: aliasKey }), { method: "DELETE" });
      toast.success(t("projects.aliasDeleted"));
      refresh();
    } catch (err) {
      toast.error(err?.payload?.error || err?.message || t("common.actionFailed"));
    } finally {
      setBusy(false);
    }
  };
  // Attribution is filled by the continuous sweep, not by a one-shot request:
  // this button restarts that sweep from the newest trace and the worker walks
  // history in the background at its own pace. The progress and the throttle
  // live on 系统 → 分析队列.
  const backfill = async () => {
    if (!window.confirm(t("projects.backfillConfirm"))) return;
    setBusy(true);
    try {
      await postJSON(apiPaths.analysisQueueResetBackfill, {});
      toast.success(t("projects.backfillStarted"));
      refresh();
    } catch (err) {
      toast.error(err?.payload?.error || err?.message || t("common.actionFailed"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <section className="hero-grid">
        <StatCard label={t("projects.title")} value={data?.total ?? 0} />
        <StatCard label={t("projects.identifiedSessions")} value={identified} />
        <StatCard
          label={t("projects.coverage")}
          value={`${coverage.toFixed(1)}%`}
          title={`${formatRawNumber(identified)} / ${formatRawNumber(totalSessions)}`}
          accent={coverage >= 80 ? "accent-green" : undefined}
        />
        <StatCard label={t("projects.totalSessions")} value={totalSessions} />
        <p className="system-note">{t("projects.coverageNote")}</p>
      </section>

      <Card as="section">
        <div className="panel-head">
          <div>
            <h2>{t("projects.recentTitle")}</h2>
          </div>
          <div className="panel-head-actions">
            {canAdmin ? (
              <Button variant="ghost" type="button" onClick={backfill} disabled={busy}>
                {t("projects.backfill")}
              </Button>
            ) : null}
            <Pager page={data?.page ?? page} totalPages={Math.max(data?.total_pages ?? 1, 1)} onChange={goToPage} />
          </div>
        </div>
        <form className="filter-bar" onSubmit={applyFilters}>
          <Input
            className="min-w-[260px]"
            type="search"
            placeholder={t("projects.search")}
            value={filters.query}
            onChange={(event) => setFilters((current) => ({ ...current, query: event.target.value }))}
          />
          <Button variant="ghost" type="submit">
            {t("common.apply")}
          </Button>
          <Button variant="ghost" type="button" onClick={resetFilters}>
            {t("common.reset")}
          </Button>
        </form>

        {error ? <EmptyState title={t("projects.loadError")} detail={error} tone="danger" /> : null}
        {loading && !data ? <EmptyState title={t("projects.loading")} detail={t("projects.loadingDetail")} /> : null}

        {items.length ? (
          <div className="project-table">
            <div className="project-table-head">
              <span>{t("projects.directory")}</span>
              <span>{t("projects.sessions")}</span>
              <span>{t("common.requests")}</span>
              <span>{t("common.tokens")}</span>
              <span>{t("projects.actions")}</span>
            </div>
            {items.map((item) => (
              <div key={item.root_key} className="project-row">
                <button type="button" className="project-open" onClick={() => openProject(item.root_key)} title={t("projects.openSessions")}>
                  <span className="project-path-cell">
                    <strong className="mono project-path">{item.root}</strong>
                    <span className="trace-tag-group">
                      {item.path_count > 1 ? (
                        <InlineTag tone="accent" title={t("projects.mergedPaths")}>
                          {item.path_count} {t("projects.paths")}
                        </InlineTag>
                      ) : null}
                      {(item.sources || []).map((source) => (
                        <InlineTag key={source}>{source}</InlineTag>
                      ))}
                    </span>
                  </span>
                </button>
                <span className="trace-metric-stack">
                  <strong>{item.session_count}</strong>
                  <span>{formatDateTime(item.last_seen)}</span>
                </span>
                <span className="trace-metric-stack">
                  <strong>{item.request_count}</strong>
                </span>
                <span className="trace-metric-stack">
                  <strong title={String(item.total_tokens ?? 0)}>{formatTokenCount(item.total_tokens ?? 0)}</strong>
                </span>
                <span className="action-group">
                  {canAdmin ? (
                    <Button variant="ghost" size="sm" type="button" onClick={() => openMerge(item)}>
                      {t("projects.merge")}
                    </Button>
                  ) : null}
                </span>
              </div>
            ))}
          </div>
        ) : null}
        {!loading && !error && !items.length ? (
          <EmptyState title={t("projects.noFound")} detail={t("projects.noFoundDetail")} />
        ) : null}
      </Card>

      <Card as="section">
        <div className="panel-head">
          <div>
            <h2>{t("projects.detectionTitle")}</h2>
            <p className="system-note">{t("projects.detectionNote")}</p>
          </div>
          <div className="panel-head-actions">
            <span className="project-detection-count">
              {t("projects.detectionDeclared")} <strong>{detectionData.declared ?? 0}</strong>
            </span>
            <span className={`project-detection-count${rejected > 0 ? " is-warn" : ""}`}>
              {t("projects.detectionRejected")} <strong>{rejected}</strong>
            </span>
          </div>
        </div>
        {(detectionData.by_reason || []).length ? (
          <div className="project-detection-reasons">
            {(detectionData.by_reason || []).map((reason) => (
              <InlineTag key={reason.label} tone="default" title={t("projects.detectionReasonHint")}>
                {reason.label} · {reason.count}
              </InlineTag>
            ))}
          </div>
        ) : null}
        {(detectionData.recent || []).length ? (
          <ul className="project-rejection-list">
            {(detectionData.recent || []).map((item) => (
              <li key={item.trace_id} className="project-rejection">
                <span className="mono">{item.reason}</span>
                <span className="project-rejection-evidence mono" title={item.evidence}>
                  {item.evidence || "—"}
                </span>
                <span className="trace-subline">
                  {item.harness || t("projects.unknownHarness")} · {formatDateTime(item.recorded_at)}
                </span>
                <a className="project-rejection-link" href={`/traces/${encodeURIComponent(item.trace_id)}`}>
                  {t("projects.detectionOpenTrace")}
                </a>
              </li>
            ))}
          </ul>
        ) : (
          <p className="system-note">{t("projects.detectionEmpty")}</p>
        )}
        {aliasItems.length ? (
          <div className="project-alias-list">
            <h3>{t("projects.aliasesTitle")}</h3>
            {aliasItems.map((alias) => (
              <div key={alias.alias_key} className="project-alias-row">
                <span className="mono">{alias.alias_key}</span>
                <span className="trace-subline">→ {alias.canonical_root || alias.canonical_key}</span>
                {canAdmin ? (
                  <Button variant="ghost" size="sm" type="button" disabled={busy} onClick={() => deleteAlias(alias.alias_key)}>
                    {t("projects.aliasDelete")}
                  </Button>
                ) : null}
              </div>
            ))}
          </div>
        ) : null}
      </Card>

      <Dialog open={Boolean(mergeSource)} onOpenChange={(open) => (open ? null : closeMerge())}>
        {mergeSource ? (
          <DialogContent className="project-merge-modal">
            <form onSubmit={submitMerge}>
              <DialogHeader>
                <div>
                  <p className="eyebrow">{t("projects.mergeTitle")}</p>
                  <DialogTitle className="mono">{mergeSource.root}</DialogTitle>
                </div>
                <DialogClose asChild>
                  <Button variant="default" size="icon" type="button" aria-label={t("common.close")}>
                    x
                  </Button>
                </DialogClose>
              </DialogHeader>
              <p className="system-note">{t("projects.mergeNote")}</p>
              <label className="project-merge-label">
                {t("projects.mergeTarget")}
                <select
                  className="filter-input filter-select"
                  value={mergeTarget}
                  onChange={(event) => setMergeTarget(event.target.value)}
                  required
                >
                  <option value="">{t("projects.mergeSelect")}</option>
                  {targetItems.map((item) => (
                    <option key={item.root_key} value={item.root_key}>
                      {item.root}
                    </option>
                  ))}
                </select>
              </label>
              <DialogFooter>
                <Button variant="ghost" type="button" onClick={closeMerge} disabled={busy}>
                  {t("common.cancel")}
                </Button>
                <Button variant="default" type="submit" disabled={busy || !mergeTarget}>
                  {t("projects.mergeConfirm")}
                </Button>
              </DialogFooter>
            </form>
          </DialogContent>
        ) : null}
      </Dialog>
    </>
  );
}
