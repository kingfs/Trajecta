import React from "react";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { Copy, ExternalLink, KeyRound, X } from "lucide-react";
import { toast } from "sonner";
import { Button } from "../ui/button";
import { InlineTag } from "../common/Badges";
import { Switch } from "../common/Controls";
import { useI18n } from "../../lib/i18n";
import { copyText } from "../../lib/clipboard";
import {
  localizedName,
  localizedDescription,
  planDisplayName,
  planDisplayDescription,
  orderedEndpoints,
} from "../../lib/plugins";

const PROTOCOL_LABEL_KEYS = {
  chat_completions: "providers.interfaceChat",
  responses: "providers.interfaceResponses",
  messages: "providers.interfaceMessages",
  gemini: "providers.interfaceGemini",
};

function EndpointRow({ protocol, url }) {
  const { t } = useI18n();
  const copy = async () => {
    if (await copyText(url)) {
      toast.success(t("common.copied"));
    }
  };
  return (
    <div className="plugin-endpoint-row">
      <span className="plugin-endpoint-protocol">{t(PROTOCOL_LABEL_KEYS[protocol] || protocol)}</span>
      <code className="plugin-endpoint-url" title={url}>{url}</code>
      <button type="button" className="plugin-copy" onClick={copy} aria-label={t("common.copy")} title={t("common.copy")}>
        <Copy size={12} aria-hidden="true" />
      </button>
    </div>
  );
}

// PluginDetailDrawer is the right-side sheet behind a plugin card (and the link
// icon in the channel creation dialog). It answers "what does this vendor
// offer": description, docs/key links, the per-protocol API bases and every
// plan's endpoints. It is informational; the enable switch is optional so the
// creation dialog can open it read-only without losing the form behind it.
export function PluginDetailDrawer({ facts, open, onClose, enabled = false, onToggle, toggleDisabled = false, escapeBlocked = false }) {
  const { t, language } = useI18n();
  if (!facts) return null;
  const defaultEndpoints = orderedEndpoints(facts.endpoints);
  return (
    <DialogPrimitive.Root open={open} onOpenChange={(next) => (next ? undefined : onClose())}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="nav-modal-backdrop plugin-drawer-backdrop" />
        <DialogPrimitive.Content
          className="plugin-drawer"
          aria-describedby={undefined}
          // A dialog stacked above this drawer (the disable confirmation) owns
          // Escape while it is open; guard the mount race window the same way
          // the creation dialog guards this drawer.
          onEscapeKeyDown={(event) => { if (escapeBlocked) event.preventDefault(); }}
        >
          <header className="plugin-drawer-head">
            <div className="plugin-drawer-head-name">
              <p className="eyebrow">{t("plugins.tabChannels")}</p>
              <DialogPrimitive.Title className="plugin-drawer-title">
                {localizedName(facts, language)}
                {facts.region ? <InlineTag tone="default">{t(`plugins.region.${facts.region}`)}</InlineTag> : null}
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
            {localizedDescription(facts, language) ? (
              <p className="plugin-drawer-desc">{localizedDescription(facts, language)}</p>
            ) : null}
            <div className="plugin-drawer-links">
              {facts.docs_url ? (
                <a className="plugin-link-btn" href={facts.docs_url} target="_blank" rel="noreferrer">
                  <ExternalLink size={13} aria-hidden="true" />
                  <span>{t("plugins.docs")}</span>
                </a>
              ) : null}
              {facts.keys_url ? (
                <a className="plugin-link-btn" href={facts.keys_url} target="_blank" rel="noreferrer">
                  <KeyRound size={13} aria-hidden="true" />
                  <span>{t("plugins.keys")}</span>
                </a>
              ) : null}
            </div>

            <section className="plugin-drawer-section">
              <h3>{t("plugins.interfaces")}</h3>
              <p className="trace-subline plugin-drawer-section-hint">{t("plugins.defaultEndpoints")}</p>
              <div className="plugin-endpoint-list">
                {defaultEndpoints.map(([protocol, url]) => (
                  <EndpointRow key={protocol} protocol={protocol} url={url} />
                ))}
              </div>
              {facts.models?.no_list ? <p className="trace-subline plugin-drawer-note">{t("providers.noModelListHint")}</p> : null}
            </section>

            {facts.plans?.length ? (
              <section className="plugin-drawer-section">
                <h3>{t("plugins.plans")}</h3>
                <div className="plugin-plan-list">
                  {facts.plans.map((plan) => (
                    <div className="plugin-plan-card" key={plan.id}>
                      <div className="plugin-plan-head">
                        <strong>{planDisplayName(plan, language)}</strong>
                        {plan.coding ? <InlineTag tone="accent">{t("providers.codingPlan")}</InlineTag> : null}
                        {plan.key_prefix ? <code className="plugin-key-prefix" title={plan.key_prefix}>{plan.key_prefix}…</code> : null}
                      </div>
                      {planDisplayDescription(plan, language) ? (
                        <p className="trace-subline plugin-plan-desc">{planDisplayDescription(plan, language)}</p>
                      ) : null}
                      <div className="plugin-endpoint-list">
                        {orderedEndpoints(plan.endpoints).map(([protocol, url]) => (
                          <EndpointRow key={protocol} protocol={protocol} url={url} />
                        ))}
                      </div>
                    </div>
                  ))}
                </div>
              </section>
            ) : null}
          </div>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}