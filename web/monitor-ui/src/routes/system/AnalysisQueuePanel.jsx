import React, { useEffect, useState } from "react";
import { Card } from "../../components/ui/card";
import { Button } from "../../components/ui/button";
import { Input } from "../../components/ui/input";
import { Switch } from "../../components/ui/switch";
import { EmptyState } from "../../components/common/EmptyState";
import { StatCard } from "../../components/common/Display";
import { InlineTag } from "../../components/common/Badges";
import { apiPaths, patchJSON, postJSON, requestJSON } from "../../lib/api";
import { useI18n } from "../../lib/i18n";
import { formatDateTime, formatRawNumber } from "../../lib/monitor";
import { useWriteMutation } from "../../lib/mutations";

/**
 * The parse queue: two lanes, one policy.
 *
 * Fresh traffic (the trace the proxy just recorded) and the historical sweep
 * (re-deriving project attribution for old cassettes) have different urgency
 * and different volume, so they are claimed separately and paced separately.
 * Nothing here is a one-shot job: the sweep tops its own queue up while it walks
 * backwards through recorded_at, and stops on its own at the oldest trace.
 *
 * The values are stored in `app_settings`, not YAML, because the right rate
 * depends on the deployment's disk and database headroom right now. A save
 * reaches the running worker before the response lands, so slowing a sweep down
 * does not need a restart.
 */
export function AnalysisQueuePanel() {
  const { t } = useI18n();
  const [form, setForm] = useState(null);
  const [view, setView] = useState(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState("");

  const load = () => {
    setLoading(true);
    requestJSON(apiPaths.settingsAnalysisQueue)
      .then((payload) => {
        setView(payload);
        setForm(formFromPayload(payload));
        setLoadError("");
      })
      .catch((error) => setLoadError(error.message))
      .finally(() => setLoading(false));
  };

  useEffect(() => {
    let cancelled = false;
    requestJSON(apiPaths.settingsAnalysisQueue)
      .then((payload) => {
        if (cancelled) return;
        setView(payload);
        setForm(formFromPayload(payload));
        setLoadError("");
      })
      .catch((error) => {
        if (!cancelled) setLoadError(error.message);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const update = (key, value) => setForm((current) => ({ ...current, [key]: value }));
  const save = useWriteMutation({
    mutationFn: () => patchJSON(apiPaths.settingsAnalysisQueue, payloadFromForm(form)),
    success: "system.queueSaved",
    error: "system.queueSaveFailed",
    onSuccess: (payload) => {
      setView(payload);
      setForm(formFromPayload(payload));
    },
  });
  const resetBackfill = useWriteMutation({
    mutationFn: () => postJSON(apiPaths.analysisQueueResetBackfill, {}),
    success: "system.queueResetDone",
    error: "system.queueSaveFailed",
    onSuccess: (payload) => {
      setView(payload);
      setForm(formFromPayload(payload));
    },
  });

  if (loadError) {
    return <Card as="section"><EmptyState title={t("system.queueUnavailable")} detail={loadError} tone="danger" compact /></Card>;
  }
  if (loading || !form || !view) {
    return <Card as="section"><p className="system-note">{t("system.loading")}</p></Card>;
  }

  const depth = view.queue || {};
  const backfill = view.backfill || {};
  const completed = Boolean(backfill.completed_at);
  const cursorAt = backfill.project_through || backfill.completed_at;

  return (
    <Card as="section">
      <div className="panel-head">
        <div>
          <h2>{t("system.queueTitle")}</h2>
          <p className="system-note">{t("system.queueIntro")}</p>
        </div>
        <div className="trace-tag-group">
          {view.runtime_attached ? <InlineTag tone="green">{t("system.queueAttached")}</InlineTag> : <InlineTag tone="gold">{t("system.queueDetached")}</InlineTag>}
          {completed ? <InlineTag tone="accent">{t("system.queueBackfillComplete")}</InlineTag> : null}
        </div>
      </div>

      <div className="hero-grid hero-grid-compact">
        <StatCard label={t("system.queueFreshQueued")} value={formatRawNumber(depth.fresh_queued || 0)} />
        <StatCard label={t("system.queueBackfillQueued")} value={formatRawNumber(depth.backfill_queued || 0)} />
        <StatCard label={t("system.queueRunning")} value={formatRawNumber(depth.running || 0)} />
        <StatCard label={t("system.queueFailed")} value={formatRawNumber(depth.failed || 0)} accent={(depth.failed || 0) > 0 ? "accent-red" : ""} />
        <StatCard label={t("system.queueAttributed")} value={formatRawNumber(depth.attributed || 0)} />
      </div>

      <form
        className="provider-form provider-form-modal"
        onSubmit={(event) => {
          event.preventDefault();
          save.mutate();
        }}
      >
        <h3 className="provider-form-wide">{t("system.queueFreshSection")}</h3>
        <label>
          {t("system.queueBatchSize")}
          <Input
            type="number"
            min="1"
            max="500"
            value={form.parse_batch_size}
            onChange={(event) => update("parse_batch_size", event.target.value)}
            disabled={save.isPending}
          />
          <span className="system-note">{t("system.queueBatchSizeHint")}</span>
        </label>
        <label>
          {t("system.queueInterval")}
          <Input
            type="number"
            min="200"
            step="100"
            value={form.parse_interval_ms}
            onChange={(event) => update("parse_interval_ms", event.target.value)}
            disabled={save.isPending}
          />
          <span className="system-note">{t("system.queueIntervalHint")}</span>
        </label>
        <label>
          {t("system.queueMaxAttempts")}
          <Input
            type="number"
            min="1"
            max="20"
            value={form.parse_max_attempts}
            onChange={(event) => update("parse_max_attempts", event.target.value)}
            disabled={save.isPending}
          />
          <span className="system-note">{t("system.queueMaxAttemptsHint")}</span>
        </label>

        <h3 className="provider-form-wide">{t("system.queueBackfillSection")}</h3>
        <label className="provider-form-check provider-form-wide">
          <Switch
            checked={form.backfill_enabled}
            onCheckedChange={(checked) => update("backfill_enabled", checked)}
            aria-label={t("system.queueBackfillEnabled")}
            disabled={save.isPending}
          />
          <span>{t("system.queueBackfillEnabled")}</span>
        </label>
        <label>
          {t("system.queueBackfillBatchSize")}
          <Input
            type="number"
            min="1"
            max="200"
            value={form.backfill_batch_size}
            onChange={(event) => update("backfill_batch_size", event.target.value)}
            disabled={save.isPending}
          />
        </label>
        <label>
          {t("system.queueBackfillInterval")}
          <Input
            type="number"
            min="200"
            step="100"
            value={form.backfill_interval_ms}
            onChange={(event) => update("backfill_interval_ms", event.target.value)}
            disabled={save.isPending}
          />
        </label>
        <label>
          {t("system.queueBackfillChunk")}
          <Input
            type="number"
            min="1"
            max="5000"
            value={form.backfill_chunk_size}
            onChange={(event) => update("backfill_chunk_size", event.target.value)}
            disabled={save.isPending}
          />
          <span className="system-note">{t("system.queueBackfillChunkHint")}</span>
        </label>
        <label>
          {t("system.queueBackfillWatermark")}
          <Input
            type="number"
            min="0"
            max="10000"
            value={form.backfill_low_watermark}
            onChange={(event) => update("backfill_low_watermark", event.target.value)}
            disabled={save.isPending}
          />
          <span className="system-note">{t("system.queueBackfillWatermarkHint")}</span>
        </label>

        <div className="provider-form-actions provider-form-wide">
          <Button variant="primary" type="submit" disabled={save.isPending}>
            {t("common.save")}
          </Button>
          <Button
            variant="ghost"
            type="button"
            disabled={resetBackfill.isPending}
            onClick={() => {
              if (window.confirm(t("system.queueResetConfirm"))) resetBackfill.mutate();
            }}
          >
            {t("system.queueResetBackfill")}
          </Button>
          {save.isPending || resetBackfill.isPending ? <InlineTag tone="gold">{t("common.saving")}</InlineTag> : null}
        </div>
      </form>

      <p className="system-note">
        {cursorAt
          ? t(completed ? "system.queueCursorComplete" : "system.queueCursor", { time: formatDateTime(cursorAt) })
          : t("system.queueCursorIdle")}
      </p>
      <p className="system-note">{t("system.queueScope")}</p>
    </Card>
  );
}

function formFromPayload(payload) {
  return {
    parse_batch_size: payload?.parse_batch_size ?? 10,
    parse_interval_ms: payload?.parse_interval_ms ?? 5000,
    parse_max_attempts: payload?.parse_max_attempts ?? 3,
    backfill_enabled: Boolean(payload?.backfill_enabled),
    backfill_batch_size: payload?.backfill_batch_size ?? 2,
    backfill_interval_ms: payload?.backfill_interval_ms ?? 5000,
    backfill_chunk_size: payload?.backfill_chunk_size ?? 1000,
    backfill_low_watermark: payload?.backfill_low_watermark ?? 200,
  };
}

function payloadFromForm(form) {
  return {
    parse_batch_size: Number(form.parse_batch_size),
    parse_interval_ms: Number(form.parse_interval_ms),
    parse_max_attempts: Number(form.parse_max_attempts),
    backfill_enabled: form.backfill_enabled,
    backfill_batch_size: Number(form.backfill_batch_size),
    backfill_interval_ms: Number(form.backfill_interval_ms),
    backfill_chunk_size: Number(form.backfill_chunk_size),
    backfill_low_watermark: Number(form.backfill_low_watermark),
  };
}
