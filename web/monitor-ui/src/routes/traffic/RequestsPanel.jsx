import React, { useEffect, useState } from "react";
import { Input } from "../../components/ui/input";
import { Button } from "../../components/ui/button";
import { useSearchParams } from "react-router-dom";
import { StatCard } from "../../components/common/Display";
import { EmptyState } from "../../components/common/EmptyState";
import { RequestList } from "../../components/monitor/RequestList";
import { useJSON } from "../../hooks/useJSON";
import { apiPaths, apiURL } from "../../lib/api";
import { useI18n } from "../../lib/i18n";
import { formatDuration, formatTime, formatTokenCount, setOrDeleteParam } from "../../lib/monitor";

const REFRESH_MS = 60_000;
const PAGE_SIZE = 50;

export function RequestsPanel() {
  const { t } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const page = Math.max(1, Number(searchParams.get("page") || "1"));
  const query = searchParams.get("q") || "";
  const provider = searchParams.get("provider") || "";
  const model = searchParams.get("model") || "";
  const observation = searchParams.get("observation") || "";
  const [filters, setFilters] = useState({ query, provider, model, observation });
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
  if (observation) {
    requestParams.set("observation", observation);
  }
  const { loading, data, error } = useJSON(apiURL(apiPaths.traces, requestParams), [page, query, provider, model, observation], { refetchInterval: REFRESH_MS });

  useEffect(() => {
    setFilters({ query, provider, model, observation });
  }, [query, provider, model, observation]);

  const items = data?.items ?? [];
  const stats = data?.stats ?? {};
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
    setOrDeleteParam(next, "observation", filters.observation);
    setSearchParams(next);
  };
  const resetFilters = () => {
    setFilters({ query: "", provider: "", model: "", observation: "" });
    const next = new URLSearchParams(searchParams);
    next.set("page", "1");
    next.delete("q");
    next.delete("provider");
    next.delete("model");
    next.delete("observation");
    setSearchParams(next);
  };

  return (
    <>
      <section className="hero-grid">
        <StatCard label={t("common.total")} value={stats.total_request ?? 0} />
        <StatCard label={t("common.avgTtft")} value={formatDuration(stats.avg_ttft ?? 0)} title={`${stats.avg_ttft ?? 0} ms`} />
        <StatCard label={t("common.tokens")} value={formatTokenCount(stats.total_tokens ?? 0)} accent="accent-gold" title={String(stats.total_tokens ?? 0)} />
        <StatCard label={t("common.success")} value={`${Number(stats.success_rate ?? 0).toFixed(1)}%`} accent="accent-green" />
      </section>

      <section className="panel">
        <div className="panel-head">
          <div>
            <h2>{t("requests.recentTitle")}</h2>
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
          <Input
            className="min-w-[260px]"
            type="search"
            placeholder={t("requests.search")}
            value={filters.query}
            onChange={(event) => setFilters((current) => ({ ...current, query: event.target.value }))}
          />
          <Input
            className="min-w-[180px]"
            type="text"
            placeholder={t("sessions.provider")}
            value={filters.provider}
            onChange={(event) => setFilters((current) => ({ ...current, provider: event.target.value }))}
          />
          <Input
            className="min-w-[180px]"
            type="text"
            placeholder={t("sessions.model")}
            value={filters.model}
            onChange={(event) => setFilters((current) => ({ ...current, model: event.target.value }))}
          />
          <select
            className="filter-input filter-select"
            value={filters.observation}
            onChange={(event) => setFilters((current) => ({ ...current, observation: event.target.value }))}
            aria-label={t("requests.observationStatus")}
          >
            <option value="">{t("requests.allObservations")}</option>
            <option value="unparsed">{t("requests.unparsed")}</option>
            <option value="parsed">{t("requests.parsed")}</option>
            <option value="failed">{t("requests.parseFailed")}</option>
            <option value="queued">{t("requests.parseQueued")}</option>
            <option value="running">{t("requests.parseRunning")}</option>
          </select>
          <Button variant="ghost" type="submit">
            {t("common.apply")}
          </Button>
          <Button variant="ghost" type="button" onClick={resetFilters}>
            {t("common.reset")}
          </Button>
        </form>

        {error ? <EmptyState title={t("requests.loadError")} detail={error} tone="danger" /> : null}
        {loading && !data ? <EmptyState title={t("requests.loading")} detail={t("requests.loadingDetail")} /> : null}

        <RequestList items={items} fromView="requests" />
      </section>
    </>
  );
}
