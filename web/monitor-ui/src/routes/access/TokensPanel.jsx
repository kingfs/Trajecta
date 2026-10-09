import React, { useMemo, useState } from "react";
import { Card } from "../../components/ui/card";
import { Input } from "../../components/ui/input";
import { Button } from "../../components/ui/button";
import { InlineTag, PlusIcon } from "../../components/common/Badges";
import { StatCard } from "../../components/common/Display";
import { EmptyState } from "../../components/common/EmptyState";
import { useJSON } from "../../hooks/useJSON";
import { useRefresh } from "../../hooks/useRefresh";
import { apiPaths, apiURL, deleteJSON, postJSON, requestJSON } from "../../lib/api";
import { useI18n } from "../../lib/i18n";
import { useWriteMutation } from "../../lib/mutations";
import { formatDateTime } from "../../lib/monitor";

export function TokensPanel() {
  const refresh = useRefresh();
  const { t } = useI18n();
  const [name, setName] = useState("local-dev");
  const [ttl, setTTL] = useState("");
  const [scope, setScope] = useState("api");
  const [created, setCreated] = useState(null);
  const [showAll, setShowAll] = useState(false);
  const tokens = useJSON(apiPaths.authTokens, []);
  const items = tokens.data?.items || [];
  const visibleItems = showAll ? items : items.filter((item) => item.status === "active");
  const summary = useMemo(() => summarizeTokens(items), [items]);

  // Success is quiet: creating a token reveals it and the table refetches, so the
  // result is already on screen. The three failures share one surface, which is
  // the toast now instead of a paragraph under the form.
  const createToken = useWriteMutation({
    mutationFn: () => postJSON(apiPaths.authTokens, { name, ttl, scope }),
    error: "tokens.createError",
    onSuccess: (payload) => {
      setCreated(payload);
      refresh();
    },
  });

  const revokeToken = useWriteMutation({
    mutationFn: ({ tokenID }) => requestJSON(`${apiPaths.authTokens}/${encodeURIComponent(tokenID)}`, { method: "DELETE" }),
    error: "tokens.revokeError",
    onSuccess: refresh,
  });

  const deleteToken = useWriteMutation({
    mutationFn: ({ tokenID }) => deleteJSON(apiURL(`${apiPaths.authTokens}/${encodeURIComponent(tokenID)}`, { delete: "1" })),
    error: "tokens.deleteError",
    onSuccess: refresh,
  });

  const submitToken = (event) => {
    event.preventDefault();
    setCreated(null);
    createToken.mutate();
  };

  const confirmDelete = (item) => {
    if (window.confirm(t("tokens.deleteConfirm", { name: item.name || item.id }))) {
      deleteToken.mutate({ tokenID: item.id });
    }
  };

  const busyToken = revokeToken.isPending ? revokeToken.variables?.tokenID : deleteToken.isPending ? deleteToken.variables?.tokenID : 0;

  return (
    <>
      <section className="hero-grid hero-grid-compact token-summary-grid">
        <StatCard label={t("common.total")} value={summary.total} />
        <StatCard label={t("common.active")} value={summary.active} accent="accent-green" />
        <StatCard label={t("common.expired")} value={summary.expired} accent={summary.expired ? "accent-gold" : ""} />
        <StatCard label={t("common.revoked")} value={summary.revoked} accent={summary.revoked ? "accent-red" : ""} />
      </section>

      <Card as="section" className="token-panel">
        <div className="panel-head">
          <div>
            <h2>{t("tokens.create")}</h2>
          </div>
        </div>
        <p className="system-note">{`${t("tokens.currentUser")} · ${t("tokens.scopeHint")}`}</p>
        <form className="token-form" onSubmit={submitToken}>
          <label className="token-field" htmlFor="token-name">
            <span>{t("tokens.name")}</span>
            <Input id="token-name" type="text" value={name} onChange={(event) => setName(event.target.value)} />
          </label>
          <label className="token-field" htmlFor="token-ttl">
            <span>{t("tokens.ttl")}</span>
            <Input id="token-ttl" type="text" placeholder={t("tokens.ttlPlaceholder")} value={ttl} onChange={(event) => setTTL(event.target.value)} />
          </label>
          <label className="token-field" htmlFor="token-scope">
            <span>{t("tokens.scope")}</span>
            <Input id="token-scope" type="text" value={scope} onChange={(event) => setScope(event.target.value)} />
          </label>
          <Button variant="default" size="icon" className="token-create-button" type="submit" disabled={createToken.isPending} title={createToken.isPending ? t("tokens.creating") : t("tokens.create")} aria-label={createToken.isPending ? t("tokens.creating") : t("tokens.create")}>
            <PlusIcon />
          </Button>
        </form>
        
        {created?.token ? (
          <div className="token-result">
            <span>{t("tokens.shownOnce")}</span>
            <code>{created.token}</code>
            <small>{t("tokens.prefixStored", { prefix: created.prefix || "-" })}</small>
          </div>
        ) : null}
      </Card>

      <Card as="section">
        <div className="panel-head">
          <div>
            <h2>{showAll ? t("tokens.allTokens") : t("tokens.activeTokens")}</h2>
          </div>
          <div className="panel-head-actions">
            <span className="badge">{t("tokens.activeBadge", { count: summary.active })}</span>
            <span className="badge">{t("tokens.totalBadge", { count: summary.total })}</span>
            <Button variant={showAll ? "primary" : "ghost"} type="button" onClick={() => setShowAll((value) => !value)}>
              {showAll ? t("tokens.showActive") : t("tokens.showAll")}
            </Button>
          </div>
        </div>
        <p className="system-note">{t("tokens.lifecycleHint")}</p>
        {tokens.error ? <EmptyState title={t("tokens.loadError")} detail={tokens.error} tone="danger" /> : null}
        {tokens.loading && !tokens.data ? <EmptyState title={t("tokens.loading")} detail={t("tokens.loadingDetail")} /> : null}
        {tokens.data ? <TokenTable items={visibleItems} busyToken={busyToken} onRevoke={(tokenID) => revokeToken.mutate({ tokenID })} onDelete={confirmDelete} /> : null}
      </Card>
    </>
  );
}

function TokenTable({ items, busyToken, onRevoke, onDelete }) {
  const { t } = useI18n();
  if (!items.length) {
    return <EmptyState title={t("tokens.noTokens")} detail={t("tokens.noTokensDetail")} />;
  }
  return (
    <div className="token-table">
      <div className="token-table-head">
        <span>{t("tokens.name")}</span>
        <span>{t("tokens.prefix")}</span>
        <span>{t("tokens.scope")}</span>
        <span>{t("common.status")}</span>
        <span>{t("common.created")}</span>
        <span>{t("tokens.expires")}</span>
        <span>{t("tokens.lastUsed")}</span>
        <span>{t("common.actions")}</span>
      </div>
      {items.map((item) => (
        <article className="token-row" key={item.id}>
          <strong>{item.name || "api-token"}</strong>
          <code>{item.prefix || "-"}</code>
          <span>{item.scope || "all"}</span>
          <InlineTag tone={statusTone(item.status)}>{item.status || "unknown"}</InlineTag>
          <span>{formatDateTime(item.created_at)}</span>
          <span>{item.expires_at ? formatDateTime(item.expires_at) : t("common.never")}</span>
          <span>{item.last_used_at ? formatDateTime(item.last_used_at) : t("common.never")}</span>
          <div className="action-group">
            <Button variant="ghost" type="button" disabled={item.status !== "active" || busyToken === item.id} onClick={() => onRevoke(item.id)}>
              {busyToken === item.id ? t("tokens.revoking") : t("tokens.revoke")}
            </Button>
            <Button variant="ghost" type="button" disabled={busyToken === item.id} onClick={() => onDelete(item)}>
              {busyToken === item.id ? t("tokens.deleting") : t("tokens.delete")}
            </Button>
          </div>
        </article>
      ))}
    </div>
  );
}

function summarizeTokens(items) {
  return items.reduce(
    (summary, item) => {
      summary.total += 1;
      if (item.status === "active") {
        summary.active += 1;
      } else if (item.status === "expired") {
        summary.expired += 1;
      } else if (item.status === "revoked") {
        summary.revoked += 1;
      }
      return summary;
    },
    { total: 0, active: 0, expired: 0, revoked: 0 },
  );
}

function statusTone(status) {
  switch (status) {
    case "active":
      return "green";
    case "expired":
      return "gold";
    case "revoked":
      return "danger";
    default:
      return "default";
  }
}
