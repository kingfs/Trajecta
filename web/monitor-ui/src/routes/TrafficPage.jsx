import React from "react";
import { useSearchParams } from "react-router-dom";
import { TabbedPage } from "../components/TabbedPage";
import { WindowToggle } from "../components/common/Tabs";
import { RequestsPanel } from "./traffic/RequestsPanel";
import { SessionsPanel } from "./traffic/SessionsPanel";
import { useI18n } from "../lib/i18n";
import { normalizeListWindow, setOrDeleteParam } from "../lib/monitor";

// 请求 and 会话 were two top-level nav entries pointing at the same data at two
// different grains, and /traces and /requests were two URLs rendering the exact
// same component. They are now two tabs of one page, one nav entry, one URL.
export function TrafficPage() {
  const { t } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const windowValue = normalizeListWindow(searchParams.get("window"));
  const setWindow = (nextWindow) => {
    const next = new URLSearchParams(searchParams);
    // The default is written as an absent parameter, so a link to the page does
    // not carry `window=all` in every URL.
    setOrDeleteParam(next, "window", nextWindow === "all" ? "" : nextWindow);
    next.delete("page");
    setSearchParams(next);
  };
  // Both tabs read the same window, so both tabs carry the same control in the
  // header rather than one each inside their own panel.
  const actions = <WindowToggle value={windowValue} onChange={setWindow} label={t("common.windowLabel")} />;
  return (
    <TabbedPage
      title={t("nav.traffic")}
      tabs={[
        { id: "requests", label: t("traffic.tabRequests"), element: <RequestsPanel />, actions },
        { id: "sessions", label: t("traffic.tabSessions"), element: <SessionsPanel />, actions },
      ]}
    />
  );
}
