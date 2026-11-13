import React from "react";
import { InlineTag } from "../common/Badges";
import { Switch } from "../common/Controls";
import { useI18n } from "../../lib/i18n";

/**
 * The one control both plugin screens render: an enable switch for a toggleable
 * plugin, or an always-on badge for compiled infrastructure (the credential/PII
 * union machine). Keeping this in one component is what prevents the two
 * screens from disagreeing about which plugins can be switched off.
 */
export function DetectorPluginSwitch({ item, disabled = false, onChange, label }) {
  const { t } = useI18n();
  if (item.always_on) {
    return <InlineTag tone="green">{t("plugins.detectionAlwaysOn")}</InlineTag>;
  }
  return (
    <Switch
      checked={Boolean(item.enabled)}
      disabled={disabled}
      onChange={onChange}
      label={label || t("plugins.enableSwitch")}
    />
  );
}
