import React, { useEffect, useMemo, useState } from "react";
import { Card } from "../components/ui/card";
import { Input } from "../components/ui/input";
import { Button } from "../components/ui/button";
import { Link, useSearchParams } from "react-router-dom";
import { StatCard } from "../components/common/Display";
import { PageHeader } from "../components/common/PageHeader";
import { WindowToggle } from "../components/common/Tabs";
import { InlineTag } from "../components/common/Badges";
import { EmptyState } from "../components/common/EmptyState";
import { Pager } from "../components/common/Pager";
import { useJSON } from "../hooks/useJSON";
import { apiPaths, apiURL } from "../lib/api";
import { useI18n } from "../lib/i18n";
import {
  buildModelLink,
  formatCount,
  formatDateTime,
  normalizeAnalyticsWindow,
  setOrDeleteParam,
} from "../lib/monitor";

const PAGE_SIZE = 18;

export function ModelsPage() {
  const { t } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const windowValue = normalizeAnalyticsWindow(searchParams.get("window"));
  const queryValue = searchParams.get("q") || "";
  const page = Math.max(1, Number.parseInt(searchParams.get("page") || "1", 10) || 1);
  const [queryDraft, setQueryDraft] = useState(queryValue);
  const params = new URLSearchParams();
  params.set("window", windowValue);
  if (queryValue) {
    params.set("q", queryValue);
  }
  const models = useJSON(apiURL(apiPaths.models, params), [windowValue, queryValue]);
  const items = models.data?.items || [];
  const totals = useMemo(() => summarizeModels(items), [items]);
  const totalPages = Math.max(1, Math.ceil(items.length / PAGE_SIZE));
  const currentPage = Math.min(page, totalPages);
  const pageItems = useMemo(() => items.slice((currentPage - 1) * PAGE_SIZE, currentPage * PAGE_SIZE), [items, currentPage]);

  useEffect(() => {
    setQueryDraft(queryValue);
  }, [queryValue]);

  // A filter or a window change can leave the URL on a page that no longer
  // exists. Clamp it once the new list arrives instead of showing an empty grid.
  useEffect(() => {
    if (!models.data || page <= totalPages) {
      return;
    }
    const next = new URLSearchParams(searchParams);
    if (totalPages <= 1) {
      next.delete("page");
    } else {
      next.set("page", String(totalPages));
    }
    setSearchParams(next, { replace: true });
  }, [models.data, page, totalPages, searchParams, setSearchParams]);

  const setWindow = (nextWindow) => {
    const next = new URLSearchParams(searchParams);
    setOrDeleteParam(next, "window", nextWindow === "today" ? "" : nextWindow);
    next.delete("page");
    setSearchParams(next);
  };
  const applySearch = (event) => {
    event.preventDefault();
    const next = new URLSearchParams(searchParams);
    setOrDeleteParam(next, "q", queryDraft);
    next.delete("page");
    setSearchParams(next);
  };

  return (
    <div className="shell shell-list">
      <PageHeader
        title={t("models.title")}
        actions={<WindowToggle value={windowValue} onChange={setWindow} label={t("models.window")} />}
      />

      <section className="hero-grid" aria-label={t("models.catalogTitle")}>
        <StatCard label={t("models.title")} value={formatCount(items.length)} />
        <StatCard label={t("common.requests")} value={formatCount(totals.requests)} />
        <StatCard label={t("common.failed")} value={formatCount(totals.failed)} accent={totals.failed ? "accent-red" : ""} />
        <StatCard label={t("common.tokens")} value={formatCount(totals.tokens)} detail={usageCoverageDetail(totals.missing, t)} />
      </section>

      <Card as="section">
        <div className="panel-head">
          <div>
            <h2>{t("models.catalogTitle")}</h2>
          </div>
          <div className="panel-head-actions">
            <Pager page={currentPage} totalPages={totalPages} onChange={(nextPage) => setPage(searchParams, setSearchParams, nextPage)} />
          </div>
        </div>
        <form className="filter-bar" onSubmit={applySearch}>
          <Input className="min-w-[260px]" type="search" value={queryDraft} onChange={(event) => setQueryDraft(event.target.value)} placeholder={t("models.search")} />
          <Button variant="ghost" type="submit">{t("common.apply")}</Button>
          <Button 
            variant="ghost"
            type="button"
            onClick={() => {
              setQueryDraft("");
              const next = new URLSearchParams(searchParams);
              next.delete("q");
              next.delete("page");
              setSearchParams(next);
            }}
          >
            {t("common.reset")}
          </Button>
        </form>
      </Card>

      {models.error ? <EmptyState title={t("models.loadError")} detail={models.error} tone="danger" /> : null}
      {models.loading && !models.data ? <EmptyState title={t("models.loading")} detail={t("models.loadingDetail")} /> : null}

      {models.data ? (
        <section className="model-market-grid" aria-label="Model cards">
          {pageItems.length ? pageItems.map((item) => <ModelCard key={item.model} item={item} windowValue={windowValue} />) : <EmptyState title={t("models.noModels")} detail={t("models.noModelsDetail")} />}
        </section>
      ) : null}
    </div>
  );
}

function setPage(searchParams, setSearchParams, nextPage) {
  const next = new URLSearchParams(searchParams);
  if (nextPage <= 1) {
    next.delete("page");
  } else {
    next.set("page", String(nextPage));
  }
  setSearchParams(next);
}

function ModelCard({ item, windowValue }) {
  const { t } = useI18n();
  const summary = item.summary || {};
  const today = item.today || {};
  const failed = Number(summary.failed_request || 0);
  return (
    <Link className="model-market-card" to={buildModelLink(item.model, windowValue)}>
      <div className="model-market-card-head">
        <div>
          <p className="eyebrow">{t("nav.models")}</p>
          <h2>{item.display_name || item.model}</h2>
        </div>
        <div className="trace-tag-group">
          <InlineTag tone="accent">{t("models.enabled", { count: formatCount(item.enabled_channel_count || 0) })}</InlineTag>
          <InlineTag>{t("models.channels", { count: formatCount(item.channel_count || 0) })}</InlineTag>
        </div>
      </div>
      <div className="model-market-metrics">
        <Metric label={t("common.requests")} value={formatCount(summary.request_count)} />
        <Metric label={t("common.errors")} value={formatCount(failed)} danger={failed > 0} />
        <Metric label={t("common.tokens")} value={formatCount(summary.total_tokens)} detail={usageCoverageDetail(summary.missing_usage_request, t)} />
        <Metric label={t("models.today")} value={formatCount(today.total_tokens)} detail={usageCoverageDetail(today.missing_usage_request, t)} />
      </div>
      <div className="model-market-footer">
        <span title={(item.channels || []).join(" · ")}>{(item.channels || []).slice(0, 4).join(" · ") || t("models.noChannel")}</span>
        <span>{formatDateTime(summary.last_seen)}</span>
      </div>
    </Link>
  );
}

function Metric({ label, value, detail = "", danger = false }) {
  return (
    <span className={danger ? "model-market-metric model-market-metric-danger" : "model-market-metric"}>
      <span>{label}</span>
      <strong>{value}</strong>
      {detail ? <small>{detail}</small> : null}
    </span>
  );
}

function summarizeModels(items) {
  return items.reduce(
    (state, item) => {
      const summary = item.summary || {};
      state.requests += Number(summary.request_count || 0);
      state.failed += Number(summary.failed_request || 0);
      state.missing += Number(summary.missing_usage_request || 0);
      state.tokens += Number(summary.total_tokens || 0);
      return state;
    },
    { requests: 0, failed: 0, tokens: 0, missing: 0 },
  );
}

function usageCoverageDetail(missing, t = (key, values) => `${values?.count || 0} missing usage`) {
  const count = Number(missing || 0);
  return count > 0 ? t("providers.missingUsage", { count: formatCount(count) }) : "";
}
