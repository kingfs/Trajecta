import React, { useEffect, useState } from "react";
import { Card } from "../../components/ui/card";
import { Button } from "../../components/ui/button";
import { Input } from "../../components/ui/input";
import { Switch } from "../../components/ui/switch";
import { EmptyState } from "../../components/common/EmptyState";
import { InlineTag } from "../../components/common/Badges";
import { apiPaths, patchJSON, requestJSON } from "../../lib/api";
import { useI18n } from "../../lib/i18n";
import { useWriteMutation } from "../../lib/mutations";

/**
 * Outbound proxy policy.
 *
 * The fields are named after the Linux variables operators already know, and
 * the server resolves them with the same semantics: http_proxy for plain HTTP,
 * https_proxy for HTTPS with an empty value falling back to http_proxy, and
 * no_proxy as a comma-separated bypass list. It is a server-side policy rather
 * than a browser setting, so the page only edits and saves it.
 *
 * The switch is off by default. When it is off every outbound request is
 * direct; the process environment is deliberately not consulted, because a
 * shell variable would otherwise move internal upstream traffic without anyone
 * changing this page.
 */
export function NetworkProxyPanel() {
  const { t } = useI18n();
  const [form, setForm] = useState({ enabled: false, http_proxy: "", https_proxy: "", no_proxy: "" });
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState("");

  useEffect(() => {
    let cancelled = false;
    requestJSON(apiPaths.networkProxySettings)
      .then((payload) => {
        if (!cancelled) {
          setForm(settingsFromPayload(payload));
          setLoading(false);
          setLoadError("");
        }
      })
      .catch((error) => {
        if (!cancelled) {
          setLoading(false);
          setLoadError(error.message);
        }
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const update = (key, value) => setForm((current) => ({ ...current, [key]: value }));
  // The stored copy wins: the server canonicalizes a bare host:port into a
  // full proxy URL, and the page shows what the transport will actually use.
  const save = useWriteMutation({
    mutationFn: () => patchJSON(apiPaths.networkProxySettings, form),
    success: "system.proxySaved",
    error: "system.proxyUnavailable",
    onSuccess: (payload) => setForm(settingsFromPayload(payload)),
  });

  return (
    <Card as="section">
      <div className="panel-head">
        <div>
          <h2>{t("system.networkProxy")}</h2>
          <p className="system-note">{t("system.proxyIntro")}</p>
        </div>
        {form.enabled ? <InlineTag tone="green">{t("system.proxyOn")}</InlineTag> : <InlineTag tone="gold">{t("system.proxyOff")}</InlineTag>}
      </div>

      {loadError ? <EmptyState title={t("system.proxyUnavailable")} detail={loadError} tone="danger" compact /> : null}
      {!loadError && loading ? <p className="system-note">{t("system.loading")}</p> : null}

      {!loadError ? (
        <form
          className="provider-form provider-form-modal"
          onSubmit={(event) => {
            event.preventDefault();
            save.mutate();
          }}
        >
          <label className="provider-form-check provider-form-wide">
            <Switch
              checked={form.enabled}
              onCheckedChange={(checked) => update("enabled", checked)}
              aria-label={t("system.proxyEnabled")}
              disabled={save.isPending}
            />
            <span>{t("system.proxyEnabled")}</span>
          </label>

          <label>
            {t("system.proxyHTTP")}
            <Input
              className="font-mono"
              value={form.http_proxy}
              onChange={(event) => update("http_proxy", event.target.value)}
              placeholder="http://127.0.0.1:7890"
              spellCheck={false}
              autoComplete="off"
            />
          </label>

          <label>
            {t("system.proxyHTTPS")}
            <Input
              className="font-mono"
              value={form.https_proxy}
              onChange={(event) => update("https_proxy", event.target.value)}
              placeholder={form.http_proxy.trim() || "http://127.0.0.1:7890"}
              spellCheck={false}
              autoComplete="off"
            />
          </label>

          <label className="provider-form-wide">
            {t("system.proxyNoProxy")}
            <Input
              className="font-mono"
              value={form.no_proxy}
              onChange={(event) => update("no_proxy", event.target.value)}
              placeholder="localhost,127.0.0.1,10.0.0.0/8,.corp.example.com,api.internal:8080"
              spellCheck={false}
              autoComplete="off"
            />
            <span className="system-note">{t("system.proxyNoProxyHint")}</span>
          </label>

          <div className="provider-form-actions provider-form-wide">
            <Button variant="primary" type="submit" disabled={save.isPending || loading}>
              {t("common.save")}
            </Button>
            {save.isPending ? <InlineTag tone="gold">{t("common.saving")}</InlineTag> : null}
          </div>
        </form>
      ) : null}

      <p className="system-note">{t("system.proxyScope")}</p>
    </Card>
  );
}

function settingsFromPayload(payload) {
  return {
    enabled: Boolean(payload?.enabled),
    http_proxy: payload?.http_proxy || "",
    https_proxy: payload?.https_proxy || "",
    no_proxy: payload?.no_proxy || "",
  };
}
