import React, { useEffect, useState } from "react";
import { Button } from "../../components/ui/button";
import { useSearchParams } from "react-router-dom";
import { StatCard } from "../../components/common/Display";
import { EmptyState } from "../../components/common/EmptyState";
import { SessionList } from "../../components/monitor/SessionList";
import { useJSON } from "../../hooks/useJSON";
import { apiPaths, apiURL } from "../../lib/api";
import { useI18n } from "../../lib/i18n";
import { formatTime, setOrDeleteParam, summarizeSessionItems } from "../../lib/monitor";

const REFRESH_MS = 60_000;
const PAGE_SIZE = 50;

export function SessionsPanel() {
  const { t } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const page = Math.max(1, Number(searchParams.get("page") || "1"));
  const query = searchParams.get("q") || "";
  const provider = searchParams.get("provider") || "";
  const model = searchParams.get("model") || "";
  const [filters, setFilters] = useState({ query, provider, model });
  const requestParams = new URLSearchParams({
    page: String(page),
    page_size: String(PAGE_SIZE),
  });
  if (query) {
    requestParams.set("q", query);
  }
  if (provider) {
    requestParams.set("provider", provider);
  }
  if (model) {
    requestParams.set("model", model);
  }
  const { loading, data, error } = useJSON(apiURL(apiPaths.sessions, requestParams), [page, query, provider, model], { refetchInterval: REFRESH_MS });

  useEffect(() => {
    setFilters({ query, provider, model });
  }, [query, provider, model]);

  const items = data?.items ?? [];
  const sessionStats = summarizeSessionItems(items);
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
    setOrDeleteParam(next, "provider", filters.provider);
    setOrDeleteParam(next, "model", filters.model);
    setSearchParams(next);
  };
  const resetFilters = () => {
    setFilters({ query: "", provider: "", model: "" });
    const next = new URLSearchParams(searchParams);
    next.set("page", "1");
    next.delete("q");
    next.delete("provider");
    next.delete("model");
    setSearchParams(next);
  };

  return (
    <>
      <section className="hero-grid">
        <StatCard label={t("sessions.title")} value={sessionStats.totalSessions} />
        <StatCard label={`${t("sessions.recentTitle")} · ${t("common.requests")}`} value={sessionStats.totalRequests} />
        <StatCard label={`${t("sessions.recentTitle")} · ${t("common.tokens")}`} value={sessionStats.totalTokens} accent="accent-gold" />
        <StatCard label={`${t("sessions.recentTitle")} · ${t("sessions.avgSuccess")}`} value={`${sessionStats.avgSuccessRate.toFixed(1)}%`} accent="accent-green" />
        <p className="system-note">{t("sessions.pageScope")}</p>
      </section>

      <section className="panel">
        <div className="panel-head">
          <div>
            <h2>{t("sessions.recentTitle")}</h2>
          </div>
          <div className="panel-head-actions">
            <span className="badge badge-live">{t("common.refresh60")}</span>
            <span className="badge">{data?.refreshed_at ? formatTime(data.refreshed_at) : "..."}</span>
            <div className="pager">
              <Button variant="ghost" disabled={page <= 1} onClick={() => goToPage(page - 1)}>
                {t("common.previous")}
              </Button>
              <span className="pager-label">
                {data?.page ?? page} / {Math.max(data?.total_pages ?? 1, 1)}
              </span>
              <Button variant="ghost" disabled={!data || page >= (data.total_pages || 1)} onClick={() => goToPage(page + 1)}>
                {t("common.next")}
              </Button>
            </div>
          </div>
        </div>
        <form className="filter-bar" onSubmit={applyFilters}>
          <input
            className="filter-input filter-input-wide"
            type="search"
            placeholder={t("sessions.search")}
            value={filters.query}
            onChange={(event) => setFilters((current) => ({ ...current, query: event.target.value }))}
          />
          <input
            className="filter-input"
            type="text"
            placeholder={t("sessions.provider")}
            value={filters.provider}
            onChange={(event) => setFilters((current) => ({ ...current, provider: event.target.value }))}
          />
          <input
            className="filter-input"
            type="text"
            placeholder={t("sessions.model")}
            value={filters.model}
            onChange={(event) => setFilters((current) => ({ ...current, model: event.target.value }))}
          />
          <Button variant="ghost" type="submit">
            {t("common.apply")}
          </Button>
          <Button variant="ghost" type="button" onClick={resetFilters}>
            {t("common.reset")}
          </Button>
        </form>

        {error ? <EmptyState title={t("sessions.loadError")} detail={error} tone="danger" /> : null}
        {loading && !data ? <EmptyState title={t("sessions.loading")} detail={t("sessions.loadingDetail")} /> : null}

        <SessionList items={items} />
      </section>
    </>
  );
}
