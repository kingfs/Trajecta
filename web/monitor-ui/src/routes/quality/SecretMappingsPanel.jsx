import React, { useEffect, useState } from "react";
import { ShieldOff, Trash2 } from "lucide-react";
import { useSearchParams } from "react-router-dom";
import { Card } from "../../components/ui/card";
import { Button } from "../../components/ui/button";
import { Input } from "../../components/ui/input";
import { EmptyState } from "../../components/common/EmptyState";
import { Pager } from "../../components/common/Pager";
import { DetailMetaPill, InlineTag } from "../../components/common/Badges";
import { useJSON } from "../../hooks/useJSON";
import { useRefresh } from "../../hooks/useRefresh";
import { apiPaths, apiURL, deleteJSON, postJSON } from "../../lib/api";
import { useI18n } from "../../lib/i18n";
import { useWriteMutation } from "../../lib/mutations";
import { setOrDeleteParam } from "../../lib/monitor";
import { formatBytes, formatCount } from "../system/format";

const PAGE_SIZE = 25;

/**
 * The global credential-mapping table.
 *
 * The trace audit tab answers "what stood in for what in this exchange"; this
 * page answers the other question: which values does the installation already
 * recognize, and which fake did it assign them? The rows come from the same
 * bbolt file the proxy loads at startup, not from a per-trace table, so a false
 * positive can be found even after its trace was pruned.
 *
 * Deletion is live. It removes the pair from the running Vault as well as from
 * the file, and the row can come back the next time the detector matches the
 * same value - the page removes a mapping, not a rule.
 */
export function SecretMappingsPanel() {
  const { t } = useI18n();
  const refresh = useRefresh();
  const [searchParams, setSearchParams] = useSearchParams();
  const page = Math.max(1, Number.parseInt(searchParams.get("page") || "1", 10) || 1);
  const q = searchParams.get("q") || "";
  const [query, setQuery] = useState(q);

  useEffect(() => {
    setQuery(q);
  }, [q]);

  const params = new URLSearchParams({ page: String(page), page_size: String(PAGE_SIZE) });
  if (q) {
    params.set("q", q);
  }
  const mappings = useJSON(apiURL(apiPaths.secretMappings, params), [page, q]);
  const data = mappings.data;
  const items = data?.items || [];
  const total = data?.total ?? 0;
  const totalPages = Math.max(1, data?.total_pages ?? 1);
  const store = data?.store || null;
  const available = data?.available !== false;

  // Deleting the last row of the last page leaves the URL pointing past the
  // served range; the server clamps the response, but the pager reads the query
  // parameter. Pull the URL back to the page that still exists.
  useEffect(() => {
    if (data && page > totalPages) {
      const next = new URLSearchParams(searchParams);
      if (totalPages <= 1) {
        next.delete("page");
      } else {
        next.set("page", String(totalPages));
      }
      setSearchParams(next, { replace: true });
    }
  }, [data, page, totalPages, searchParams, setSearchParams]);

  const removeMapping = useWriteMutation({
    mutationFn: (seq) => deleteJSON(apiPaths.secretMapping(seq)),
    success: (result) => t("quality.mappings.deleted", { seq: result?.seq ?? "-" }),
    error: "quality.mappings.deleteError",
    onSuccess: () => refresh(),
  });
  const suppressMapping = useWriteMutation({
    mutationFn: (item) =>
      postJSON(apiPaths.secretSuppressions, {
        rule: "",
        real: item.real,
        reason: t("quality.mappings.suppressReason"),
      }),
    success: "quality.mappings.suppressed",
    error: "quality.mappings.suppressError",
    onSuccess: () => refresh(),
  });
  const busySeq = removeMapping.isPending ? removeMapping.variables : null;
  const suppressingSeq = suppressMapping.isPending ? suppressMapping.variables?.seq : null;
  const rowBusy = (seq) => busySeq === seq || suppressingSeq === seq;

  const applySearch = (event) => {
    event.preventDefault();
    const next = new URLSearchParams(searchParams);
    setOrDeleteParam(next, "q", query.trim());
    next.delete("page");
    setSearchParams(next);
  };
  const resetSearch = () => {
    setQuery("");
    const next = new URLSearchParams(searchParams);
    next.delete("q");
    next.delete("page");
    setSearchParams(next);
  };
  const setPage = (nextPage) => {
    const next = new URLSearchParams(searchParams);
    if (nextPage <= 1) {
      next.delete("page");
    } else {
      next.set("page", String(nextPage));
    }
    setSearchParams(next);
  };
  const confirmDelete = (item) => {
    if (window.confirm(t("quality.mappings.deleteConfirm", { seq: item.seq }))) {
      removeMapping.mutate(item.seq);
    }
  };
  const confirmSuppress = (item) => {
    if (window.confirm(t("quality.mappings.suppressConfirm"))) {
      suppressMapping.mutate(item);
    }
  };

  if (mappings.error) {
    return <EmptyState title={t("quality.mappings.loadError")} detail={mappings.error} tone="danger" />;
  }
  if (mappings.loading && !data) {
    return <EmptyState title={t("quality.mappings.loading")} detail={t("quality.mappings.loadingDetail")} />;
  }
  if (data && !available) {
    return (
      <Card as="section">
        <div className="panel-head">
          <div>
            <h2>{t("quality.mappings.title")}</h2>
            <span className="trace-subline">{t("quality.mappings.subtitle")}</span>
          </div>
        </div>
        <EmptyState
          title={t("quality.mappings.unavailable")}
          detail={t("quality.mappings.unavailableDetail")}
          tone="default"
        />
      </Card>
    );
  }

  return (
    <Card as="section">
      <div className="panel-head">
        <div>
          <h2>{t("quality.mappings.title")}</h2>
          <span className="trace-subline">{t("quality.mappings.subtitle")}</span>
        </div>
        <div className="panel-head-actions">
          <InlineTag tone={total ? "gold" : "default"}>
            {q
              ? t("quality.mappings.matchCount", { count: total })
              : t("quality.mappings.count", { count: total })}
          </InlineTag>
          <Button variant="ghost" type="button" onClick={() => refresh()}>
            {t("common.refresh")}
          </Button>
          <Pager page={page} totalPages={totalPages} onChange={setPage} />
        </div>
      </div>

      <p className="system-note">{t("quality.mappings.hint")}</p>

      {store ? (
        <div className="detail-meta-strip">
          <DetailMetaPill label={t("quality.mappings.storePath")} value={store.path || "-"} mono />
          <DetailMetaPill label={t("quality.mappings.fileSize")} value={formatBytes(store.file_size_bytes)} />
          <DetailMetaPill label={t("quality.mappings.entries")} value={formatCount(store.entry_count)} />
          <DetailMetaPill label={t("quality.mappings.encryptedRows")} value={formatCount(store.encrypted_rows)} />
          <DetailMetaPill label={t("quality.mappings.plaintextRows")} value={formatCount(store.plaintext_rows)} />
          {store.unreadable_rows ? (
            <DetailMetaPill label={t("quality.mappings.unreadableRows")} value={formatCount(store.unreadable_rows)} />
          ) : null}
        </div>
      ) : null}

      <form className="filter-bar" onSubmit={applySearch}>
        <Input
          className="min-w-[220px]"
          type="search"
          placeholder={t("quality.mappings.searchPlaceholder")}
          value={query}
          onChange={(event) => setQuery(event.target.value)}
        />
        <Button variant="ghost" type="submit">
          {t("quality.mappings.search")}
        </Button>
        <Button variant="ghost" type="button" onClick={resetSearch}>
          {t("common.reset")}
        </Button>
      </form>

      {items.length ? (
        <div className="credential-mask-list">
          {items.map((item) => (
            <article className="finding-card secret-mapping-card" key={item.seq}>
              <div className="finding-card-head">
                <div>
                  <strong className="mono">#{item.seq}</strong>
                  <span>
                    {t("quality.mappings.length", {
                      real: item.real_length ?? 0,
                      fake: item.fake_length ?? 0,
                    })}
                  </span>
                </div>
                <div className="trace-tag-group">
                  {item.encrypted ? (
                    <InlineTag tone="green">{t("quality.mappings.encrypted")}</InlineTag>
                  ) : (
                    <InlineTag tone="gold">{t("quality.mappings.plaintext")}</InlineTag>
                  )}
                  {item.decode_error ? <InlineTag tone="danger">{t("quality.mappings.unreadableTag")}</InlineTag> : null}
                  {!item.decode_error ? (
                    <Button
                      variant="ghost"
                      size="sm"
                      disabled={rowBusy(item.seq)}
                      onClick={() => confirmSuppress(item)}
                    >
                      <ShieldOff size={13} aria-hidden="true" />
                      <span>{t("quality.mappings.suppress")}</span>
                    </Button>
                  ) : null}
                  <Button
                    variant="danger"
                    size="sm"
                    disabled={rowBusy(item.seq)}
                    onClick={() => confirmDelete(item)}
                  >
                    <Trash2 size={13} aria-hidden="true" />
                    <span>{t("quality.mappings.delete")}</span>
                  </Button>
                </div>
              </div>
              {item.decode_error ? (
                <p className="credential-mask-missing">{t("quality.mappings.unreadableDetail")}</p>
              ) : (
                <div className="credential-mask-pair">
                  <div className="credential-mask-value">
                    <span className="credential-mask-label">{t("quality.mappings.real")}</span>
                    <code className="credential-mask-code">{item.real}</code>
                  </div>
                  <div className="credential-mask-value">
                    <span className="credential-mask-label">{t("quality.mappings.fake")}</span>
                    <code className="credential-mask-code">{item.fake}</code>
                  </div>
                </div>
              )}
            </article>
          ))}
        </div>
      ) : (
        <EmptyState
          title={q ? t("quality.mappings.emptySearch") : t("quality.mappings.empty")}
          detail={t("quality.mappings.emptyDetail")}
          action={q ? <Button variant="ghost" type="button" onClick={resetSearch}>{t("common.reset")}</Button> : null}
        />
      )}
    </Card>
  );
}
