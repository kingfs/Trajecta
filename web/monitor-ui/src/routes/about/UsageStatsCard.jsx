import React, { useEffect, useState } from "react";
import { Badge } from "../../components/ui/badge";
import { Card } from "../../components/ui/card";
import { EmptyState } from "../../components/common/EmptyState";
import { useI18n } from "../../lib/i18n";
import { formatDateTime } from "../../lib/monitor";
import { INSTALL_PING_EVENT, lastInstallPing } from "../../lib/telemetry";
import { SystemFact } from "../system/format";

/*
 * 使用统计: what the anonymous install ping does, stated while looking at it.
 *
 * The switch is a server-side setting, so this card cannot turn it off. What it
 * can do - and what it is for - is answer "is it on right now, what exactly goes
 * out, and when did this browser last send it", so that nobody has to read the
 * configuration file or take the documentation's word for it. A telemetry
 * feature an operator cannot inspect from the product is a feature they are
 * right not to trust.
 */
export function UsageStatsCard({ about }) {
  const { t } = useI18n();
  const settings = about?.telemetry || {};
  const [lastPing, setLastPing] = useState(() => lastInstallPing());

  useEffect(() => {
    const refresh = () => setLastPing(lastInstallPing());
    window.addEventListener(INSTALL_PING_EVENT, refresh);
    // Also read once after mount: the report that App fires on load may land
    // between this component's first render and the listener being attached.
    const timer = window.setTimeout(refresh, 2000);
    return () => {
      window.removeEventListener(INSTALL_PING_EVENT, refresh);
      window.clearTimeout(timer);
    };
  }, []);

  return (
    <Card as="section">
      <div className="panel-head">
        <div>
          <h2>{t("about.telemetry.title")}</h2>
          <p className="system-note">{t("about.telemetry.description")}</p>
        </div>
        <Badge tone={settings.enabled ? "info" : "muted"}>
          {settings.enabled ? t("about.telemetry.on") : t("about.telemetry.off")}
        </Badge>
      </div>

      {!settings.enabled ? (
        <EmptyState title={t("about.telemetry.off")} detail={t("about.telemetry.offDetail")} />
      ) : (
        <>
          <p className="system-note">{t("about.telemetry.sendsDetail")}</p>
          <div className="system-kv about-telemetry-facts">
            <SystemFact label={t("about.telemetry.installId")} value={about?.install_id} />
            <SystemFact label={t("about.telemetry.provider")} value={settings.provider} />
            <SystemFact label={t("about.telemetry.endpoint")} value={settings.endpoint} />
            <SystemFact
              label={t("about.telemetry.interval")}
              value={formatInterval(t, settings.interval_seconds)}
            />
            <SystemFact
              label={t("about.telemetry.lastPing")}
              value={lastPing ? `${formatDateTime(new Date(lastPing.at).toISOString())} · ${lastPing.version}` : t("about.telemetry.never")}
            />
          </div>
          {!about?.install_id ? <p className="system-note">{t("about.telemetry.noInstallId")}</p> : null}
        </>
      )}
    </Card>
  );
}

function formatInterval(t, seconds) {
  const value = Number(seconds || 0);
  if (!Number.isFinite(value) || value <= 0) {
    return "";
  }
  if (value % 3600 === 0) {
    return t("about.telemetry.intervalHours", { count: value / 3600 });
  }
  return t("about.telemetry.intervalSeconds", { count: value });
}
