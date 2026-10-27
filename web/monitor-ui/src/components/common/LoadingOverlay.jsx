import React from "react";
import { useIsFetching } from "@tanstack/react-query";
import { useDelayedFlag } from "../../hooks/useDelayedFlag";
import { useI18n } from "../../lib/i18n";

// A 30-day list, a cold session trajectory or a database-backed audit table can
// take seconds, and a window or filter change keeps the previous response on
// screen while the next one is in flight. With nothing moving, "still loading"
// and "loaded, and this is the answer" looked identical - the reader could only
// wait and guess.
//
// The mask is driven by the query cache rather than by a flag each page has to
// remember to pass: every read in the console goes through useJSON, so one
// component here covers the events feed, traffic lists, audit panels and detail
// pages alike. It only counts a read that is still mounted and has nothing to
// show yet (`type: "active"` and `state.data === undefined`): a new key, an
// initial load or a filter change. A realtime push refetches a query that
// already has data on screen, and that refresh must not put the scrim back over
// content the reader is already using - that is what made a trace detail page
// flash "loading" as unrelated traffic was recorded. The delay in
// useDelayedFlag keeps it out of the way of the quick filter changes.
export function PageLoadingMask() {
  const { t } = useI18n();
  const fetching = useIsFetching({ type: "active", predicate: (query) => query.state.data === undefined }) > 0;
  const visible = useDelayedFlag(fetching, 200);

  return (
    <div className={`page-loading-mask${visible ? " page-loading-mask-visible" : ""}`} aria-hidden={visible ? "false" : "true"}>
      <div className="page-loading-pill" role="status" aria-live="polite">
        <span className="page-loading-spinner" aria-hidden="true" />
        <span>{t("common.loading")}</span>
      </div>
    </div>
  );
}
