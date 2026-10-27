import React from "react";
import { Card } from "../../components/ui/card";
import { Input } from "../../components/ui/input";
import { Button } from "../../components/ui/button";
import { useSearchParams } from "react-router-dom";
import { EmptyState } from "../../components/common/EmptyState";
import { InlineTag } from "../../components/common/Badges";
import { ResourceTree } from "../../components/monitor/ResourceTree";
import { useJSON } from "../../hooks/useJSON";
import { apiPaths, apiURL, postJSON } from "../../lib/api";
import { useI18n } from "../../lib/i18n";
import { useRefresh } from "../../hooks/useRefresh";
import { useWriteMutation } from "../../lib/mutations";
import { normalizeAnalyticsWindow, setOrDeleteParam } from "../../lib/monitor";

/*
 * The cross-trace resource catalogue, drawn as directory trees. A path read by a
 * hundred requests is one leaf here, so the page answers "what has this
 * deployment touched, and how often" without growing with traffic, and the tree
 * makes the directories it worked in visible at a glance. The per-trace relation
 * lives on the trace detail page.
 *
 * The catalogue is served in pages; the tree is built from the page the API
 * returns, so the hint under the toolbar reports when the tree does not cover
 * the whole prefix set. Counting requests instead of steps is what makes a
 * shared path sort by how many traces actually read it.
 */
export function ResourcesPanel() {
  const { t } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const kind = searchParams.get("kind") || "";
  const query = searchParams.get("q") || "";
  const refresh = useRefresh();
  const windowParam = searchParams.get("window") || "";
  const window = windowParam && windowParam !== "all" ? normalizeAnalyticsWindow(windowParam) : "all";

  // The largest page the API serves: a tree is read as one picture, so it has to
  // hold as many rows as one request can carry.
  const params = new URLSearchParams({ page_size: "200" });
  if (kind) {
    params.set("kind", kind);
  }
  if (query) {
    params.set("q", query);
  }
  if (window !== "all") {
    params.set("window", window);
  }
  const resources = useJSON(apiURL(apiPaths.resources, params), [kind, query, window]);
  const items = Array.isArray(resources.data?.items) ? resources.data.items : [];
  const total = resources.data?.total ?? 0;

  // The parse worker is the only place extraction runs, so a historical trace
  // is indexed by queueing its parse job. The window picker bounds the batch.
  const backfill = useWriteMutation({
    mutationFn: () => postJSON(apiPaths.resourcesBackfill, { window, limit: 500 }),
    success: (response) => t("resources.backfillQueued", { count: response?.queued ?? 0 }),
    error: "resources.backfillError",
    onSuccess: () => refresh(),
  });

  const setFilter = (key, value) => {
    const next = new URLSearchParams(searchParams);
    setOrDeleteParam(next, key, value);
    setSearchParams(next);
  };
  const resetFilters = () => {
    const next = new URLSearchParams(searchParams);
    next.delete("kind");
    next.delete("q");
    next.delete("window");
    setSearchParams(next);
  };

  return (
    <Card as="section">
      <div className="panel-head">
        <div>
          <h2>{t("resources.title")}</h2>
          <p className="trace-subline">{t("resources.hint")}</p>
        </div>
        <div className="panel-head-actions">
          <InlineTag>{t("resources.total", { count: total })}</InlineTag>
          <Button variant="primary" type="button" disabled={backfill.isPending} onClick={() => backfill.mutate()}>
            {backfill.isPending ? t("resources.backfilling") : t("resources.backfill")}
          </Button>
        </div>
      </div>
      <form className="filter-bar" onSubmit={(event) => event.preventDefault()}>
        <Input
          className="min-w-[220px]"
          type="search"
          placeholder={t("resources.searchPlaceholder")}
          value={query}
          onChange={(event) => setFilter("q", event.target.value)}
        />
        <select className="filter-input" aria-label={t("resources.kind")} value={kind} onChange={(event) => setFilter("kind", event.target.value)}>
          <option value="">{t("resources.anyKind")}</option>
          <option value="url">{t("resources.kindUrl")}</option>
          <option value="local_path">{t("resources.kindPath")}</option>
        </select>
        <select className="filter-input" aria-label={t("common.windowLabel")} value={window} onChange={(event) => setFilter("window", event.target.value)}>
          {["all", "30d", "7d", "today"].map((option) => (
            <option key={option} value={option}>{t(`common.window.${option}`)}</option>
          ))}
        </select>
        <Button variant="ghost" type="button" onClick={resetFilters}>{t("common.reset")}</Button>
      </form>

      {items.length && total > items.length ? (
        <p className="resource-tree-partial">{t("resources.treePartial", { shown: items.length, total })}</p>
      ) : null}

      {resources.error ? <EmptyState title={t("resources.loadError")} detail={resources.error} tone="danger" /> : null}
      {resources.loading && !resources.data ? <EmptyState title={t("resources.loading")} detail={t("resources.loadingDetail")} /> : null}

      {items.length ? (
        <ResourceTree
          items={items}
          countKey="trace_count"
          t={t}
          leafHref={(item) =>
            item.last_trace_id ? `/traces/${encodeURIComponent(item.last_trace_id)}?tab=resources` : ""
          }
        />
      ) : resources.data ? (
        <EmptyState title={t("resources.none")} detail={t("resources.noneDetail")} />
      ) : null}
    </Card>
  );
}
