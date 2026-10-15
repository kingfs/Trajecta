import React, { useEffect, useMemo, useRef, useState } from "react";
import { Check, Copy } from "lucide-react";
import { toast } from "sonner";
import { Card } from "../../components/ui/card";
import { Input } from "../../components/ui/input";
import { Button } from "../../components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "../../components/ui/dialog";
import { InlineTag, PlusIcon } from "../../components/common/Badges";
import { StatCard } from "../../components/common/Display";
import { EmptyState } from "../../components/common/EmptyState";
import { useJSON } from "../../hooks/useJSON";
import { useRefresh } from "../../hooks/useRefresh";
import { apiPaths, apiURL, deleteJSON, postJSON, requestJSON } from "../../lib/api";
import { copyText } from "../../lib/clipboard";
import { useI18n } from "../../lib/i18n";
import { useWriteMutation } from "../../lib/mutations";
import { formatDateTime } from "../../lib/monitor";

// The only scope a Monitor-issued token can carry. It is still stored with the
// token, but it is not a choice and not worth a column: every row would read the
// same word.
const TOKEN_SCOPE = "api";

export function TokensPanel() {
  const refresh = useRefresh();
  const { t } = useI18n();
  const [createOpen, setCreateOpen] = useState(false);
  const [showAll, setShowAll] = useState(false);
  const tokens = useJSON(apiPaths.authTokens, []);
  const items = tokens.data?.items || [];
  const visibleItems = showAll ? items : items.filter((item) => item.status === "active");
  const summary = useMemo(() => summarizeTokens(items), [items]);

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
            <Button variant="primary" className="icon-text-button" type="button" onClick={() => setCreateOpen(true)}>
              <PlusIcon />
              <span>{t("tokens.create")}</span>
            </Button>
          </div>
        </div>
        <p className="system-note">{t("tokens.lifecycleHint")}</p>
        {tokens.error ? <EmptyState title={t("tokens.loadError")} detail={tokens.error} tone="danger" /> : null}
        {tokens.loading && !tokens.data ? <EmptyState title={t("tokens.loading")} detail={t("tokens.loadingDetail")} /> : null}
        {tokens.data ? <TokenTable items={visibleItems} busyToken={busyToken} onRevoke={(tokenID) => revokeToken.mutate({ tokenID })} onDelete={confirmDelete} /> : null}
      </Card>

      {createOpen ? <CreateTokenDialog onClose={() => setCreateOpen(false)} onCreated={refresh} /> : null}
    </>
  );
}

/**
 * The create form lives in a dialog rather than on the page. Creating a token is
 * a rare, deliberate act, and the always-mounted card it used to be pushed the
 * token list - the thing a reader actually comes back for - below the fold.
 *
 * On success the form is replaced by the secret itself, because the raw token is
 * shown exactly once. The dialog stays open until it is dismissed, so the copy
 * button never disappears out from under the reader.
 */
function CreateTokenDialog({ onClose, onCreated }) {
  const { t } = useI18n();
  const [name, setName] = useState("local-dev");
  const [expiresAt, setExpiresAt] = useState("");
  const [created, setCreated] = useState(null);
  // The floor of the picker is fixed when the dialog opens; recomputing it on
  // every render would move the minimum under the reader as the clock ticks.
  const minimumExpiry = useMemo(() => toLocalDateTimeInput(new Date(Date.now() + 60_000)), []);

  const createToken = useWriteMutation({
    // The API still speaks a Go duration. The reader picks an instant and the
    // dialog turns it into the time left; empty means a token that never expires.
    mutationFn: () => postJSON(apiPaths.authTokens, { name, scope: TOKEN_SCOPE, ttl: ttlFromExpiresAt(expiresAt) }),
    error: "tokens.createError",
    onSuccess: (payload) => {
      setCreated(payload);
      onCreated?.();
    },
  });

  const submit = (event) => {
    event.preventDefault();
    if (expiresAt && !(new Date(expiresAt).getTime() > Date.now())) {
      toast.error(t("tokens.expiryInPast"));
      return;
    }
    createToken.mutate();
  };

  return (
    <Dialog open onOpenChange={(next) => (next ? undefined : onClose())}>
      <DialogContent className="token-create-modal">
        {created?.token ? (
          <>
            <DialogHeader>
              <div>
                <p className="eyebrow">{t("tokens.title")}</p>
                <DialogTitle>{t("tokens.createdTitle")}</DialogTitle>
              </div>
              <DialogClose asChild>
                <Button variant="default" size="icon" type="button" aria-label={t("common.close")}>x</Button>
              </DialogClose>
            </DialogHeader>
            <TokenSecret token={created.token} prefix={created.prefix} />
            <DialogFooter>
              <Button variant="primary" type="button" onClick={onClose}>{t("tokens.done")}</Button>
            </DialogFooter>
          </>
        ) : (
          <form onSubmit={submit}>
            <DialogHeader>
              <div>
                <p className="eyebrow">{t("tokens.title")}</p>
                <DialogTitle>{t("tokens.create")}</DialogTitle>
              </div>
              <DialogClose asChild>
                <Button variant="default" size="icon" type="button" aria-label={t("common.close")}>x</Button>
              </DialogClose>
            </DialogHeader>
            <div className="token-create-form">
              <label className="token-field" htmlFor="token-name">
                <span>{t("tokens.name")}</span>
                <Input id="token-name" type="text" value={name} onChange={(event) => setName(event.target.value)} placeholder={t("tokens.namePlaceholder")} autoComplete="off" />
              </label>
              <label className="token-field" htmlFor="token-expiry">
                <span>{t("tokens.expires")}</span>
                <Input id="token-expiry" type="datetime-local" min={minimumExpiry} value={expiresAt} onChange={(event) => setExpiresAt(event.target.value)} />
              </label>
              <p className="token-field-hint">{t("tokens.expiresHint")}</p>
            </div>
            <DialogFooter>
              <Button variant="ghost" type="button" onClick={onClose}>{t("providers.cancel")}</Button>
              <Button variant="primary" type="submit" disabled={createToken.isPending}>{createToken.isPending ? t("tokens.creating") : t("tokens.generate")}</Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}

/**
 * The one-time secret with its copy button.
 *
 * `user-select: all` on the code box means a single click selects the whole
 * token, so a console on a plain-HTTP origin - where the clipboard API does not
 * exist and the fallback can still be blocked - always leaves a manual Ctrl+C
 * as the last resort.
 */
function TokenSecret({ token, prefix }) {
  const { t } = useI18n();
  const [copied, setCopied] = useState(false);
  const codeRef = useRef(null);
  const resetTimer = useRef(0);

  useEffect(() => () => window.clearTimeout(resetTimer.current), []);

  const copy = async () => {
    if (await copyText(token)) {
      setCopied(true);
      window.clearTimeout(resetTimer.current);
      resetTimer.current = window.setTimeout(() => setCopied(false), 2000);
      return;
    }
    selectContents(codeRef.current);
    toast.error(t("tokens.copyFailed"));
  };

  return (
    <div className="token-result">
      <span>{t("tokens.shownOnce")}</span>
      <div className="token-secret">
        <code ref={codeRef} className="token-secret-value">{token}</code>
        <Button variant="primary" className="token-copy-button" type="button" onClick={copy}>
          {copied ? <Check size={14} aria-hidden="true" /> : <Copy size={14} aria-hidden="true" />}
          <span>{copied ? t("common.copied") : t("common.copy")}</span>
        </Button>
      </div>
      <small>{t("tokens.prefixStored", { prefix: prefix || "-" })}</small>
    </div>
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

/**
 * The picked instant as the Go duration the API expects, or "" for "never
 * expires". The value is a whole number of seconds, which `time.ParseDuration`
 * reads directly.
 */
function ttlFromExpiresAt(value) {
  if (!value) {
    return "";
  }
  const target = new Date(value).getTime();
  if (!Number.isFinite(target)) {
    return "";
  }
  const seconds = Math.round((target - Date.now()) / 1000);
  return seconds > 0 ? `${seconds}s` : "";
}

/** A `datetime-local` value: local time, minutes precision, no time zone. */
function toLocalDateTimeInput(date) {
  const pad = (value) => String(value).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

function selectContents(node) {
  if (!node || typeof window === "undefined" || typeof window.getSelection !== "function") {
    return;
  }
  const range = document.createRange();
  range.selectNodeContents(node);
  const selection = window.getSelection();
  selection.removeAllRanges();
  selection.addRange(range);
}
