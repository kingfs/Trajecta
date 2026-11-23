import React, { useEffect, useState } from "react";
import { Card } from "../../components/ui/card";
import { Input } from "../../components/ui/input";
import { Button } from "../../components/ui/button";
import { useSearchParams } from "react-router-dom";
import { StatCard } from "../../components/common/Display";
import { EmptyState } from "../../components/common/EmptyState";
import { Pager } from "../../components/common/Pager";
import { InlineTag } from "../../components/common/Badges";
import { useJSON } from "../../hooks/useJSON";
import { apiPaths, apiURL } from "../../lib/api";
import { useI18n } from "../../lib/i18n";
import { formatDateTime, formatRawNumber, formatTokenCount, normalizeListWindow, setOrDeleteParam } from "../../lib/monitor";

const PAGE_SIZE = 50;

// The project list answers "how many sessions do we have per project".
//
// A project is the directory a client harness declared about itself in the
// request prompt, not a path a model touched, so a session with no declaration
// is in no project at all. The coverage numbers above the table say how much of
// the window could be attributed; a project count is never silently pretending
// to cover the sessions whose prompt carried no declaration.
export function ProjectsPanel() {
  const { t } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const page = Math.max(1, Number.parseInt(searchParams.get("page") || "1", 10) || 1);
  const query = searchParams.get("q") || "";
  const windowValue = normalizeListWindow(searchParams.get("window"));
  const [filters, setFilters] = useState({ query });
  const requestParams = new URLSearchParams({
    page: String(page),
    page_size: String(PAGE_SIZE),
  });
  if (query) {
    requestParams.set("q", query);
  }
  requestParams.set("window", windowValue);
  const { loading, data, error } = useJSON(apiURL(apiPaths.projects, requestParams), [page, query, windowValue]);

  useEffect(() => {
    setFilters({ query });
  }, [query]);

  const items = data?.items ?? [];
  const identified = data?.identified_sessions ?? 0;
  const totalSessions = data?.total_sessions ?? 0;
  const coverage = totalSessions > 0 ? (100 * identified) / totalSessions : 0;
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
  // The project row opens the session list filtered to that project, in the
  // same page: the tab and the filter both live in the query string, so the
  // view is linkable.
  const openProject = (rootKey) => {
    const next = new URLSearchParams(searchParams);
    next.set("tab", "sessions");
    next.set("project", rootKey);
    next.delete("page");
    setSearchParams(next);
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
              <span>{t("projects.lastActive")}</span>
            </div>
            {items.map((item) => (
              <button
                key={item.root_key}
                type="button"
                className="project-row"
                onClick={() => openProject(item.root_key)}
                title={t("projects.openSessions")}
              >
                <span className="project-path-cell">
                  <strong className="mono project-path">{item.root}</strong>
                  <span className="trace-tag-group">
                    {(item.sources || []).map((source) => (
                      <InlineTag key={source}>{source}</InlineTag>
                    ))}
                  </span>
                </span>
                <span className="trace-metric-stack">
                  <strong>{item.session_count}</strong>
                </span>
                <span className="trace-metric-stack">
                  <strong>{item.request_count}</strong>
                </span>
                <span className="trace-metric-stack">
                  <strong title={String(item.total_tokens ?? 0)}>{formatTokenCount(item.total_tokens ?? 0)}</strong>
                </span>
                <span className="trace-subline">{formatDateTime(item.last_seen)}</span>
              </button>
            ))}
          </div>
        ) : null}
        {!loading && !error && !items.length ? (
          <EmptyState title={t("projects.noFound")} detail={t("projects.noFoundDetail")} />
        ) : null}
      </Card>
    </>
  );
}
