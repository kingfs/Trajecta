import React, { useState } from "react";
import { Crosshair } from "lucide-react";
import { Card } from "../ui/card";
import { EmptyState } from "../common/EmptyState";
import { useJSON } from "../../hooks/useJSON";
import { credentialMaskItems, credentialMaskURLForEvent, credentialMaskURLForTrace, sortCredentialMasks } from "../../lib/credentialMasks";

/*
 * What stood in for a credential, on the two screens that ask.
 *
 * The event feed's `credential_masked` notice carries no credential bytes by
 * design, so a reader who sees "a key was replaced" had nowhere to go: the
 * events page printed the offsets, and the trace's audit tab listed findings
 * only, because both read surfaces predate `/api/secret-masks`. These two blocks
 * are that missing half. The pair comes from the audit table through the same
 * endpoint the MCP tool reads, so the console does not become a second source of
 * truth for the values.
 *
 * Both screens show the same row shape on purpose: the reader arrives either
 * from the notification ("which key was this?") or from the request ("what left
 * this machine?"), and the answer should not depend on the door they came in by.
 */

function ruleMeta(mask) {
  return [mask?.vendor, mask?.kind].filter(Boolean).join(" · ");
}

/**
 * One replacement: what the client sent, and what the upstream received.
 *
 * The labels name the direction rather than the table columns, because
 * "original" and "stand-in" only mean something once the reader knows which way
 * the request was going. The length is printed next to the pair so a reader can
 * see at a glance that the stand-in preserved it - the property the proxy
 * depends on, since it forwards the upstream Content-Length verbatim.
 */
function CredentialMaskRow({ mask, t, InlineTag, onLocate, locating = false }) {
  const original = String(mask?.original || "");
  const standIn = String(mask?.stand_in || "");
  const length = Number(mask?.value_length || original.length || standIn.length || 0);
  return (
    <article className="finding-card credential-mask-card">
      <div className="finding-card-head">
        <div>
          <strong className="mono">{mask?.rule || "-"}</strong>
          <span>{ruleMeta(mask) || "-"}</span>
        </div>
        <div className="trace-tag-group">
          {mask?.confidence ? <InlineTag tone={mask.confidence === "high" ? "danger" : "gold"}>{mask.confidence}</InlineTag> : null}
          <InlineTag>{t("masks.offset", { start: mask?.start ?? 0, end: mask?.end ?? 0 })}</InlineTag>
          {length ? <InlineTag>{t("masks.length", { length })}</InlineTag> : null}
        </div>
      </div>
      <div className="credential-mask-pair">
        <div className="credential-mask-value">
          <span className="credential-mask-label">{t("masks.original")}</span>
          <div className="credential-mask-code-row">
            {original ? <code className="credential-mask-code">{original}</code> : <span className="credential-mask-missing">{t("masks.originalUnavailable")}</span>}
            {original && onLocate ? (
              <button
                type="button"
                className="credential-mask-locate"
                title={t("masks.locateHint")}
                aria-label={t("masks.locateHint")}
                aria-busy={locating ? "true" : "false"}
                disabled={locating}
                onClick={() => onLocate(mask)}
              >
                <Crosshair size={13} aria-hidden="true" />
              </button>
            ) : null}
          </div>
        </div>
        <div className="credential-mask-value">
          <span className="credential-mask-label">{t("masks.standIn")}</span>
          {standIn ? <code className="credential-mask-code">{standIn}</code> : <span className="credential-mask-missing">{t("masks.standInUnavailable")}</span>}
        </div>
      </div>
      <div className="finding-card-where-meta credential-mask-foot">
        {[mask?.storage_mode ? t("masks.storageMode", { mode: mask.storage_mode }) : "", mask?.detected_at ? String(mask.detected_at) : ""].filter(Boolean).join(" · ")}
      </div>
    </article>
  );
}

/**
 * The list itself, with the three states a read can be in. It is separate from
 * the cards below so a caller that already owns a card (the events page) can
 * inline it.
 */
export function CredentialMaskList({ masks, loading = false, error = "", t, InlineTag, emptyTitle = "", emptyDetail = "", onLocate = null }) {
  const [locatingID, setLocatingID] = useState("");
  const locate = async (mask) => {
    if (!onLocate || locatingID) {
      return;
    }
    setLocatingID(mask.id);
    try {
      await onLocate(mask);
    } finally {
      setLocatingID("");
    }
  };
  if (error) {
    return <EmptyState title={t("masks.loadError")} detail={error} tone="danger" compact />;
  }
  if (loading && !masks) {
    return <EmptyState title={t("masks.loading")} detail={t("masks.loadingDetail")} compact />;
  }
  const items = sortCredentialMasks(credentialMaskItems(masks));
  if (!items.length) {
    return <EmptyState title={emptyTitle || t("masks.empty")} detail={emptyDetail || t("masks.emptyDetail")} compact />;
  }
  return (
    <div className="credential-mask-list">
      {items.map((mask, index) => (
        <CredentialMaskRow
          key={mask?.id || `${mask?.rule || "mask"}-${mask?.event_index ?? index}`}
          mask={mask}
          t={t}
          InlineTag={InlineTag}
          onLocate={onLocate ? locate : null}
          locating={locatingID === mask.id}
        />
      ))}
    </div>
  );
}

/**
 * The audit tab's card: every credential the proxy replaced while recording this
 * trace. It sits above the findings because it is the request's own fact - the
 * bytes that left the machine - while a finding is a detector's opinion.
 */
export function CredentialMaskSection({ traceID, t, InlineTag, onLocate }) {
  const url = credentialMaskURLForTrace(traceID);
  const masks = useJSON(url, [traceID]);
  const count = credentialMaskItems(masks.data).length;
  return (
    <Card as="section" className="audit-panel credential-mask-panel">
      <div className="panel-head">
        <div>
          <h2>{t("masks.title")}</h2>
          <span className="trace-subline">{t("masks.sectionDetail")}</span>
        </div>
        <InlineTag tone={count ? "gold" : "default"}>{t(count === 1 ? "masks.countOne" : "masks.count", { count })}</InlineTag>
      </div>
      <CredentialMaskList masks={masks.data} loading={masks.loading} error={masks.error} t={t} InlineTag={InlineTag} onLocate={onLocate} />
    </Card>
  );
}

/**
 * The events page block. A notification names a rule and a trace; this resolves
 * it to the pair the audit table holds, so "a key was masked" is answerable
 * without leaving the inbox. The caller renders it only for a credential_masked
 * event, so the read stays parked for every other notification.
 */
export function CredentialMaskEventSection({ event, t, InlineTag }) {
  const url = credentialMaskURLForEvent(event);
  const masks = useJSON(url, [event?.id]);
  return (
    <section className="credential-mask-event">
      <div className="credential-mask-event-head">
        <h3>{t("masks.eventTitle")}</h3>
        <span className="trace-subline">{t("masks.eventDetail")}</span>
      </div>
      <CredentialMaskList
        masks={masks.data}
        loading={masks.loading}
        error={masks.error}
        t={t}
        InlineTag={InlineTag}
        emptyTitle={t("masks.eventEmpty")}
        emptyDetail={t("masks.eventEmptyDetail")}
      />
    </section>
  );
}
