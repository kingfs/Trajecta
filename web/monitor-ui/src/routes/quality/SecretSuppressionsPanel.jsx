import React, { useEffect, useState } from "react";
import { Trash2 } from "lucide-react";
import { useSearchParams } from "react-router-dom";
import { Card } from "../../components/ui/card";
import { Button } from "../../components/ui/button";
import { Input } from "../../components/ui/input";
import { EmptyState } from "../../components/common/EmptyState";
import { Pager } from "../../components/common/Pager";
import { InlineTag } from "../../components/common/Badges";
import { useJSON } from "../../hooks/useJSON";
import { useRefresh } from "../../hooks/useRefresh";
import { apiPaths, apiURL, deleteJSON, postJSON } from "../../lib/api";
import { useI18n } from "../../lib/i18n";
import { useWriteMutation } from "../../lib/mutations";
import { formatDateTime, setOrDeleteParam } from "../../lib/monitor";

const PAGE_SIZE = 25;

/**
 * The false-positive list.
 *
 * A mapping delete cleans up the recorded real-to-stand-in pair; it does not
 * stop the detector from matching the value again. This page is the other
 * decision: while an entry exists, the Vault skips the matching rule/value
 * before rendering a stand-in, so no mapping, mask event or audit row is
 * produced. The rule and the note are kept so the detector can be tuned against
 * what was suppressed later.
 */
export function SecretSuppressionsPanel() {
  const { t } = useI18n();
  const refresh = useRefresh();
  const [searchParams, setSearchParams] = useSearchParams();
  const page = Math.max(1, Number.parseInt(searchParams.get("page") || "1", 10) || 1);
  const q = searchParams.get("q") || "";

  const [query, setQuery] = useState(q);
  const [rule, setRule] = useState("");
  const [real, setReal] = useState("");
  const [reason, setReason] = useState("");

  useEffect(() => {
    setQuery(q);
  }, [q]);

  const params = new URLSearchParams({ page: String(page), page_size: String(PAGE_SIZE) });
  if (q) {
    params.set("q", q);
  }
  const suppressions = useJSON(apiURL(apiPaths.secretSuppressions, params), [page, q]);
  const data = suppressions.data;
  const items = data?.items || [];
  const total = data?.total ?? 0;
  const totalPages = Math.max(1, data?.total_pages ?? 1);
  const rules = data?.rules || [];
  const available = data?.available !== false;

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

  const add = useWriteMutation({
    mutationFn: (payload) => postJSON(apiPaths.secretSuppressions, payload),
    success: "quality.suppressions.added",
    error: "quality.suppressions.addError",
    onSuccess: () => {
      setRule("");
      setReal("");
      setReason("");
      refresh();
    },
  });

  const remove = useWriteMutation({
    mutationFn: (id) => deleteJSON(apiPaths.secretSuppression(id)),
    success: "quality.suppressions.deleted",
    error: "quality.suppressions.deleteError",
    onSuccess: () => refresh(),
  });
  const busyID = remove.isPending ? remove.variables : null;

  const submit = (event) => {
    event.preventDefault();
    if (!real) {
      return;
    }
    add.mutate({ rule: rule.trim(), real, reason: reason.trim() });
  };
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
    if (window.confirm(t("quality.suppressions.deleteConfirm"))) {
      remove.mutate(item.id);
    }
  };

  if (suppressions.error) {
    return <EmptyState title={t("quality.suppressions.loadError")} detail={suppressions.error} tone="danger" />;
  }
  if (suppressions.loading && !data) {
    return <EmptyState title={t("quality.suppressions.loading")} detail={t("quality.suppressions.loadingDetail")} />;
  }
  if (data && !available) {
    return (
      <Card as="section">
        <div className="panel-head">
          <div>
            <h2>{t("quality.suppressions.title")}</h2>
            <span className="trace-subline">{t("quality.suppressions.subtitle")}</span>
          </div>
        </div>
        <EmptyState title={t("quality.suppressions.unavailable")} detail={t("quality.suppressions.unavailableDetail")} />
      </Card>
    );
  }

  return (
    <>
      <Card as="section">
        <div className="panel-head">
          <div>
            <h2>{t("quality.suppressions.addTitle")}</h2>
            <span className="trace-subline">{t("quality.suppressions.addHint")}</span>
          </div>
        </div>
        <form className="suppression-form-grid" onSubmit={submit}>
          <label className="suppression-field">
            <span className="credential-mask-label">{t("quality.suppressions.rule")}</span>
            <Input
              list="secret-suppression-rules"
              placeholder={t("quality.suppressions.rulePlaceholder")}
              value={rule}
              onChange={(event) => setRule(event.target.value)}
            />
          </label>
          <datalist id="secret-suppression-rules">
            {rules.map((name) => (
              <option key={name} value={name} />
            ))}
          </datalist>
          <label className="suppression-field">
            <span className="credential-mask-label">{t("quality.suppressions.value")}</span>
            <Input
              className="mono"
              placeholder={t("quality.suppressions.valuePlaceholder")}
              value={real}
              onChange={(event) => setReal(event.target.value)}
            />
          </label>
          <label className="suppression-field">
            <span className="credential-mask-label">{t("quality.suppressions.reason")}</span>
            <Input
              placeholder={t("quality.suppressions.reasonPlaceholder")}
              value={reason}
              onChange={(event) => setReason(event.target.value)}
            />
          </label>
          <Button variant="primary" type="submit" disabled={add.isPending || !real}>
            {add.isPending ? t("quality.suppressions.adding") : t("quality.suppressions.add")}
          </Button>
        </form>
      </Card>

      <Card as="section">
        <div className="panel-head">
          <div>
            <h2>{t("quality.suppressions.title")}</h2>
            <span className="trace-subline">{t("quality.suppressions.subtitle")}</span>
          </div>
          <div className="panel-head-actions">
            <InlineTag tone={total ? "gold" : "default"}>
              {q
                ? t("quality.suppressions.matchCount", { count: total })
                : t("quality.suppressions.count", { count: total })}
            </InlineTag>
            <Button variant="ghost" type="button" onClick={() => refresh()}>
              {t("common.refresh")}
            </Button>
            <Pager page={page} totalPages={totalPages} onChange={setPage} />
          </div>
        </div>
        <p className="system-note">{t("quality.suppressions.hint")}</p>

        <form className="filter-bar" onSubmit={applySearch}>
          <Input
            className="min-w-[220px]"
            type="search"
            placeholder={t("quality.suppressions.searchPlaceholder")}
            value={query}
            onChange={(event) => setQuery(event.target.value)}
          />
          <Button variant="ghost" type="submit">
            {t("quality.suppressions.search")}
          </Button>
          <Button variant="ghost" type="button" onClick={resetSearch}>
            {t("common.reset")}
          </Button>
        </form>

        {items.length ? (
          <div className="credential-mask-list">
            {items.map((item) => (
              <article className="finding-card" key={item.id}>
                <div className="finding-card-head">
                  <div>
                    <strong className="mono">{item.rule || t("quality.suppressions.allRules")}</strong>
                    <span>{item.reason || t("quality.suppressions.noReason")}</span>
                  </div>
                  <div className="trace-tag-group">
                    <InlineTag>#{item.id}</InlineTag>
                    {item.decode_error ? (
                      <InlineTag tone="danger">{t("quality.suppressions.decodeErrorTag")}</InlineTag>
                    ) : null}
                    <InlineTag>{formatDateTime(item.created_at)}</InlineTag>
                    <Button
                      variant="danger"
                      size="sm"
                      disabled={busyID === item.id}
                      onClick={() => confirmDelete(item)}
                    >
                      <Trash2 size={13} aria-hidden="true" />
                      <span>{t("quality.suppressions.delete")}</span>
                    </Button>
                  </div>
                </div>
                {item.decode_error ? (
                  <p className="credential-mask-missing">{t("quality.suppressions.decodeError")}</p>
                ) : (
                  <div className="credential-mask-value">
                    <span className="credential-mask-label">{t("quality.suppressions.valueLabel")}</span>
                    <code className="credential-mask-code">{item.real}</code>
                  </div>
                )}
              </article>
            ))}
          </div>
        ) : (
          <EmptyState
            title={q ? t("quality.suppressions.emptySearch") : t("quality.suppressions.empty")}
            detail={t("quality.suppressions.emptyDetail")}
            action={q ? <Button variant="ghost" type="button" onClick={resetSearch}>{t("common.reset")}</Button> : null}
          />
        )}
      </Card>
    </>
  );
}
