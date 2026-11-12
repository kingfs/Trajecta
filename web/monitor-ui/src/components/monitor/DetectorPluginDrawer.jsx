import React from "react";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import { Button } from "../ui/button";
import { InlineTag } from "../common/Badges";
import { Switch } from "../common/Controls";
import { useI18n } from "../../lib/i18n";
import {
  localizedName,
  localizedDescription,
  orderedDetectorLanes,
  detectorPIIEnabled,
} from "../../lib/plugins";

// The only plugin with a settings surface today: its opaque settings carry the
// rule-level switches (disable_pii, disabled_rules) of the compiled DFA.
const SECRETMASK_ID = "secretmask";

// DetectorPluginDrawer is the right-side sheet behind a detection-plugin card.
// It answers "what does this algorithm do and where does it run": description,
// contract/lanes facts, the rule catalog and — for secretmask — the personal
// data switch. It shares the channel drawer's chrome; the enable switch is
// optional so a read-only caller can open it without write handlers.
export function DetectorPluginDrawer({
  item,
  open,
  onClose,
  enabled = false,
  onToggle,
  toggleDisabled = false,
  onPIIToggle,
  piiToggleDisabled = false,
}) {
  const { t, language } = useI18n();
  if (!item) return null;
  const rules = Array.isArray(item.rules) ? item.rules : [];
  return (
    <DialogPrimitive.Root open={open} onOpenChange={(next) => (next ? undefined : onClose())}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="nav-modal-backdrop plugin-drawer-backdrop" />
        <DialogPrimitive.Content className="plugin-drawer" aria-describedby={undefined}>
          <header className="plugin-drawer-head">
            <div className="plugin-drawer-head-name">
              <p className="eyebrow">{t("plugins.tabDetection")}</p>
              <DialogPrimitive.Title className="plugin-drawer-title">
                {localizedName(item, language)}
                {item.kind ? <InlineTag tone="accent">{t(`plugins.kind.${item.kind}`)}</InlineTag> : null}
              </DialogPrimitive.Title>
            </div>
            <div className="plugin-drawer-head-actions">
              {onToggle ? (
                <Switch checked={Boolean(enabled)} disabled={toggleDisabled} onChange={onToggle} label={t("plugins.enableSwitch")} />
              ) : null}
              <DialogPrimitive.Close asChild>
                <Button variant="default" size="icon" type="button" aria-label={t("common.close")}>
                  <X size={14} aria-hidden="true" />
                </Button>
              </DialogPrimitive.Close>
            </div>
          </header>
          <div className="plugin-drawer-body">
            {localizedDescription(item, language) ? (
              <p className="plugin-drawer-desc">{localizedDescription(item, language)}</p>
            ) : null}

            <section className="plugin-drawer-section">
              <h3>{t("plugins.detectionRuntime")}</h3>
              <div className="detector-facts">
                <div className="detector-fact-row">
                  <span className="detector-fact-label">{t("plugins.detectionContract")}</span>
                  <InlineTag tone="default">{t(`plugins.contract.${item.contract}`)}</InlineTag>
                </div>
                <div className="detector-fact-row">
                  <span className="detector-fact-label">{t("plugins.detectionLanes")}</span>
                  <span className="detector-plugin-tags">
                    {orderedDetectorLanes(item.lanes).map((lane) => (
                      <InlineTag key={lane} tone={lane === "hot" ? "gold" : "default"}>{t(`plugins.lane.${lane}`)}</InlineTag>
                    ))}
                  </span>
                </div>
                {item.builtin ? (
                  <div className="detector-fact-row">
                    <span className="detector-fact-label">{t("plugins.detectionOrigin")}</span>
                    <InlineTag tone="green">{t("plugins.detectionBuiltin")}</InlineTag>
                  </div>
                ) : null}
              </div>
            </section>

            {item.id === SECRETMASK_ID ? (
              <section className="plugin-drawer-section">
                <h3>{t("plugins.detectionSettings")}</h3>
                <div className="detector-setting-row">
                  <div className="detector-setting-text">
                    <strong>{t("plugins.detectionPiiToggle")}</strong>
                    <p className="trace-subline">{t("plugins.detectionPiiHint")}</p>
                  </div>
                  {onPIIToggle ? (
                    <Switch
                      checked={detectorPIIEnabled(item)}
                      disabled={piiToggleDisabled}
                      onChange={onPIIToggle}
                      label={t("plugins.detectionPiiToggle")}
                    />
                  ) : null}
                </div>
              </section>
            ) : null}

            <section className="plugin-drawer-section">
              <h3>{t("plugins.detectionRules")}</h3>
              {rules.length ? (
                <>
                  <p className="trace-subline plugin-drawer-section-hint">{t("plugins.detectionRulesCount", { count: rules.length })}</p>
                  <div className="detector-rule-list">
                    {rules.map((rule) => (
                      <div className="detector-rule-row" key={rule.name}>
                        <code className="detector-rule-name" title={rule.name}>{rule.name}</code>
                        <span className="detector-rule-meta">
                          {rule.kind ? <InlineTag tone="default">{rule.kind}</InlineTag> : null}
                          {rule.vendor ? <span className="detector-rule-vendor">{rule.vendor}</span> : null}
                          {rule.report_only ? <InlineTag tone="gold">{t("plugins.detectionReportOnly")}</InlineTag> : null}
                        </span>
                      </div>
                    ))}
                  </div>
                </>
              ) : (
                <p className="trace-subline plugin-drawer-note">{t("plugins.detectionNoRules")}</p>
              )}
            </section>
          </div>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}
