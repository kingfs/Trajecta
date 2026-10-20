import React, { useCallback, useEffect, useState } from "react";
import { Sparkles } from "lucide-react";
import { Button } from "../../components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "../../components/ui/dialog";
import { EmptyState } from "../../components/common/EmptyState";
import { apiPaths, requestJSON } from "../../lib/api";
import { useI18n } from "../../lib/i18n";
import { SystemFact } from "../system/format";

/*
 * 检查新版本: one button in the hero, one dialog with the answer.
 *
 * The check used to be a card of its own, below the hero, which repeated the
 * running version and then printed the release result in the same shape. That
 * made the page say the build number twice before it said anything a reader
 * came for. The result now lives in the dialog the button opens: the running
 * version and the newest release, and nothing else.
 *
 * It is still a click rather than a read-on-mount. A Monitor is often deployed
 * where nothing may call out, and a page that silently requests api.github.com
 * every time it is opened spends the deployment's rate limit and its operator's
 * trust. Pressing the button is the consent.
 *
 * Every outcome is one shape. The endpoint answers 200 with a status even when
 * the lookup failed, so "up to date", "newer", "not comparable", "turned off"
 * and "unreachable" all render here; a thrown error means the Monitor itself
 * did not answer, which is a different sentence.
 */
export function UpdateCheckDialog({ about, onClose }) {
  const { t } = useI18n();
  const [state, setState] = useState({ status: "loading", result: null, error: "" });

  const check = useCallback(async () => {
    setState({ status: "loading", result: null, error: "" });
    try {
      const result = await requestJSON(apiPaths.versionLatest);
      setState({ status: "done", result, error: "" });
    } catch (error) {
      setState({ status: "failed", result: null, error: error?.message || String(error) });
    }
  }, []);

  useEffect(() => {
    check();
  }, [check]);

  const result = state.result;
  const retryable =
    state.status === "failed" ||
    (state.status === "done" && result && ["unavailable", "unknown"].includes(result.status));

  return (
    <Dialog open onOpenChange={(next) => (next ? undefined : onClose())}>
      <DialogContent className="about-update-modal" aria-describedby={undefined}>
        <DialogHeader>
          <div>
            <p className="eyebrow">{about?.name || "Trajecta"}</p>
            <DialogTitle>{t("about.update.title")}</DialogTitle>
          </div>
          <DialogClose asChild>
            <Button variant="default" size="icon" type="button" aria-label={t("common.close")}>
              x
            </Button>
          </DialogClose>
        </DialogHeader>

        <div className="about-update-body" aria-live="polite">
          {state.status === "failed" ? (
            <EmptyState tone="danger" title={t("about.update.monitorFailed")} detail={state.error} />
          ) : null}

          {state.status === "done" && result ? <UpdateStatus result={result} /> : null}

          {state.status === "loading" || (state.status === "done" && result) ? (
            <div className="system-kv about-update-facts">
              <SystemFact
                label={t("about.update.current")}
                value={result?.current_version || about?.version || t("about.versionUnknown")}
              />
              <SystemFact
                label={t("about.update.latest")}
                value={result ? result.latest_version : t("about.update.checking")}
              />
            </div>
          ) : null}
        </div>

        <DialogFooter>
          {retryable ? (
            <Button variant="default" type="button" onClick={check}>
              {t("about.update.retry")}
            </Button>
          ) : null}
          <Button variant="ghost" type="button" onClick={onClose}>
            {t("common.close")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function UpdateStatus({ result }) {
  const { t } = useI18n();

  if (result.status === "update_available") {
    return (
      <p className="about-update-headline">
        <Sparkles size={16} aria-hidden="true" />
        {t("about.update.available", { version: result.latest_version || "" })}
      </p>
    );
  }

  if (result.status === "up_to_date") {
    return <p className="about-update-headline about-update-headline-ok">{t("about.update.upToDate")}</p>;
  }

  if (result.status === "unknown") {
    return (
      <>
        <p className="about-update-headline">{t("about.update.unknown")}</p>
        {result.error ? <p className="system-note">{result.error}</p> : null}
      </>
    );
  }

  return (
    <EmptyState
      tone="warning"
      title={result.status === "disabled" ? t("about.update.disabled") : t("about.update.unavailable")}
      detail={result.status === "disabled" ? t("about.update.disabledDetail") : result.error || ""}
    />
  );
}
