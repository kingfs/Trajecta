import React from "react";
import { TabbedPage } from "../components/TabbedPage";
import { RequestsPanel } from "./traffic/RequestsPanel";
import { SessionsPanel } from "./traffic/SessionsPanel";
import { useI18n } from "../lib/i18n";

// 请求 and 会话 were two top-level nav entries pointing at the same data at two
// different grains, and /traces and /requests were two URLs rendering the exact
// same component. They are now two tabs of one page, one nav entry, one URL.
export function TrafficPage() {
  const { t } = useI18n();
  return (
    <TabbedPage
      eyebrow={t("nav.group.observe")}
      title={t("nav.traffic")}
      subtitle={t("traffic.subtitle")}
      tabs={[
        { id: "requests", label: t("traffic.tabRequests"), element: <RequestsPanel /> },
        { id: "sessions", label: t("traffic.tabSessions"), element: <SessionsPanel /> },
      ]}
    />
  );
}
