import React, { useEffect, useId, useMemo, useState } from "react";
import { Card } from "../../components/ui/card";
import { Input } from "../../components/ui/input";
import { Button } from "../../components/ui/button";
import { SegmentedControl, SegmentedControlItem } from "../../components/ui/segmented-control";
import { Link, useSearchParams } from "react-router-dom";
import { ArrowUpRight, ExternalLink } from "lucide-react";
import { CollapsibleCard, StatCard } from "../../components/common/Display";
import { InlineTag } from "../../components/common/Badges";
import { EmptyState } from "../../components/common/EmptyState";
import { Pager } from "../../components/common/Pager";
import { useJSON } from "../../hooks/useJSON";
import { apiPaths, apiURL } from "../../lib/api";
import { useI18n } from "../../lib/i18n";
import { ModelAliasesPanel } from "../RoutingPage";
import {
  buildModelLink,
  formatCount,
  formatTokenCount,
  normalizeAnalyticsWindow,
  setOrDeleteParam,
} from "../../lib/monitor";
import {
  modelCapabilityChips,
  modelContextWindow,
  modelDescription,
  modelDisplayName,
  modelMaxOutput,
  safeExternalURL,
} from "../../lib/modelFacts";

const PAGE_SIZE = 24;

/**
 * The model square: what this installation can route right now.
 *
 * One card per model name, listing every channel that declares it. The card is
 * enriched from Argus (the static catalogue compiled into the server) when the
 * name resolves, which is what supplies the provider, description,
 * capabilities and the link an operator follows to read the vendor's own page.
 *
 * Usage is deliberately absent here. A model with no traffic is exactly the
 * case this page exists for, so ranking cards by requests would bury it; the
 * sibling 实时统计 tab owns those numbers.
 */
export function ModelSquarePanel() {
  const { t } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const scope = searchParams.get("scope") === "all" ? "all" : "enabled";
  // The square and the usage tab are two lists on one page. They deliberately
  // do not share `q`/`page`: the square searches catalogue fields (provider,
  // description) that the usage list does not, and a search typed there used to
  // leave the usage tab empty after a switch. `square_*` keeps each tab's
  // filter and page its own.
  const queryValue = searchParams.get("square_q") || "";
  const windowValue = normalizeAnalyticsWindow(searchParams.get("window"));
  const page = Math.max(1, Number.parseInt(searchParams.get("square_page") || "1", 10) || 1);
  const [queryDraft, setQueryDraft] = useState(queryValue);

  const params = new URLSearchParams();
  if (scope === "all") {
    params.set("scope", "all");
  }
  if (queryValue) {
    params.set("square_q", queryValue);
  }
  const square = useJSON(apiURL(apiPaths.modelSquare, params), [scope, queryValue]);
  const items = square.data?.items || [];
  const summary = square.data?.summary || {};
  const totalPages = Math.max(1, Math.ceil(items.length / PAGE_SIZE));
  const currentPage = Math.min(page, totalPages);
  const pageItems = useMemo(() => items.slice((currentPage - 1) * PAGE_SIZE, currentPage * PAGE_SIZE), [items, currentPage]);

  useEffect(() => {
    setQueryDraft(queryValue);
  }, [queryValue]);

  useEffect(() => {
    if (!square.data || page <= totalPages) {
      return;
    }
    const next = new URLSearchParams(searchParams);
    if (totalPages <= 1) {
      next.delete("square_page");
    } else {
      next.set("square_page", String(totalPages));
    }
    setSearchParams(next, { replace: true });
  }, [square.data, page, totalPages, searchParams, setSearchParams]);

  const applySearch = (event) => {
    event.preventDefault();
    const next = new URLSearchParams(searchParams);
    setOrDeleteParam(next, "square_q", queryDraft);
    next.delete("square_page");
    setSearchParams(next);
  };

  const selectScope = (nextScope) => {
    const next = new URLSearchParams(searchParams);
    setOrDeleteParam(next, "scope", nextScope === "all" ? "all" : "");
    next.delete("square_page");
    setSearchParams(next);
  };

  return (
    <>
      <section className="hero-grid" aria-label={t("models.squareStatsLabel")}>
        <StatCard label={t("models.squareStatsModels")} value={formatCount(summary.models || 0)} />
        <StatCard label={t("models.squareStatsAvailable")} value={formatCount(summary.available || 0)} accent={summary.available ? "accent-green" : ""} />
        <StatCard label={t("models.squareStatsChannels")} value={formatCount(summary.channels || 0)} />
        <StatCard label={t("models.squareStatsMatched")} value={formatCount(summary.matched || 0)} detail={t("models.squareStatsMatchedDetail")} />
      </section>

      <Card as="section">
        <div className="panel-head">
          <div>
            <h2>{t("models.squareCatalogTitle")}</h2>
            <p className="trace-subline">{t("models.squareSubtitle")}</p>
          </div>
          <div className="panel-head-actions">
            <Pager page={currentPage} totalPages={totalPages} onChange={(nextPage) => setPage(searchParams, setSearchParams, nextPage)} />
          </div>
        </div>
        <form className="filter-bar" onSubmit={applySearch}>
          <Input
            className="min-w-[260px]"
            type="search"
            value={queryDraft}
            onChange={(event) => setQueryDraft(event.target.value)}
            placeholder={t("models.squareSearch")}
          />
          <SegmentedControl value={scope} onValueChange={selectScope} aria-label={t("models.squareScope")}>
            <SegmentedControlItem value="enabled" active={scope === "enabled"}>{t("models.squareScopeEnabled")}</SegmentedControlItem>
            <SegmentedControlItem value="all" active={scope === "all"}>{t("models.squareScopeAll")}</SegmentedControlItem>
          </SegmentedControl>
          <Button variant="ghost" type="submit">{t("common.apply")}</Button>
          <Button
            variant="ghost"
            type="button"
            onClick={() => {
              setQueryDraft("");
              const next = new URLSearchParams(searchParams);
              next.delete("square_q");
              next.delete("square_page");
              setSearchParams(next);
            }}
          >
            {t("common.reset")}
          </Button>
        </form>
      </Card>

      {square.error ? <EmptyState title={t("models.squareLoadError")} detail={square.error} tone="danger" /> : null}
      {square.loading && !square.data ? <EmptyState title={t("models.squareLoading")} detail={t("models.squareLoadingDetail")} /> : null}

      {square.data ? (
        pageItems.length ? (
          <section className="model-square-grid" aria-label={t("models.squareCatalogTitle")}>
            {pageItems.map((item) => <ModelSquareCard key={item.model} item={item} windowValue={windowValue} />)}
          </section>
        ) : (
          <EmptyState
            title={queryValue ? t("models.squareNoMatch") : t("models.squareEmpty")}
            detail={queryValue ? t("models.squareNoMatchDetail") : t("models.squareEmptyDetail")}
          />
        )
      ) : null}

      {/* The alias table is a model-resolution fact and lives with the model
          catalogue. It stays collapsed because most visits are about "what can
          I call", and the panel only mounts when it is opened. */}
      <CollapsibleCard title={t("routing.modelAliases")} subtitle={t("routing.modelResolution")} defaultOpen={false}>
        <ModelAliasesPanel />
      </CollapsibleCard>
    </>
  );
}

function setPage(searchParams, setSearchParams, nextPage) {
  const next = new URLSearchParams(searchParams);
  if (nextPage <= 1) {
    next.delete("square_page");
  } else {
    next.set("square_page", String(nextPage));
  }
  setSearchParams(next);
}

function ModelSquareCard({ item, windowValue }) {
  const { language, t } = useI18n();
  const titleID = useId();
  const facts = item.facts || null;
  const channels = item.channels || [];
  const protocols = Array.isArray(item.protocols) ? item.protocols : [];
  const title = modelDisplayName(item, facts);
  const description = modelDescription(facts, language);
  const contextWindow = modelContextWindow(facts, channels);
  const maxOutput = modelMaxOutput(facts, channels);
  const capabilities = modelCapabilityChips(facts, t);
  const providers = providerCount(channels);
  const officialURL = safeExternalURL(facts?.official_url);
  const modelCardURL = safeExternalURL(facts?.model_card_url);
  // The series and the configured display name are both shown only when they
  // add a distinction: repeating the card title as a "series" chip is noise.
  const relations = [];
  for (const relation of [facts?.series || facts?.family, item.display_name]) {
    const value = String(relation || "").trim();
    if (!value || value.toLowerCase() === String(title).toLowerCase() || value.toLowerCase() === item.model.toLowerCase()) {
      continue;
    }
    if (!relations.some((existing) => existing.toLowerCase() === value.toLowerCase())) {
      relations.push(value);
    }
  }
  const detailLink = buildModelLink(item.model, windowValue);
  return (
    <article className={item.available ? "model-square-card" : "model-square-card model-square-card-unavailable"}>
      <Link className="model-square-main" to={detailLink} aria-labelledby={titleID}>
        <header className="model-square-head">
          <div className="model-square-identity">
            <span className="model-square-provider">{facts?.provider || channelProviderLabel(channels, t)}</span>
            <h2 id={titleID}>{title}</h2>
            <code title={item.model}>{item.model}</code>
          </div>
          <div className="trace-tag-group">
            <InlineTag tone={item.available ? "green" : "default"}>
              {item.available ? t("models.squareAvailable") : t("models.squareUnavailable")}
            </InlineTag>
            <InlineTag tone="accent">{t("models.squareChannelCount", { count: formatCount(channels.length) })}</InlineTag>
          </div>
        </header>

        <p className={description ? "model-square-description" : "model-square-description model-square-description-empty"}>
          {description || t("models.squareNoDescription")}
        </p>

        {relations.length ? (
          <div className="model-square-relations">
            {relations.map((relation) => <span key={relation}>{relation}</span>)}
          </div>
        ) : null}

        <div className="model-square-metrics">
          <SquareMetric label={t("models.squareContext")} value={contextWindow ? formatTokenCount(contextWindow) : t("models.squareUnknown")} />
          <SquareMetric label={t("models.squareMaxOutput")} value={maxOutput ? formatTokenCount(maxOutput) : t("models.squareUnknown")} />
          <SquareMetric label={t("models.squareProviders")} value={providers ? formatCount(providers) : t("models.squareUnknown")} />
        </div>

        {protocols.length ? (
          <div className="model-square-capabilities" aria-label={t("models.squareProtocols")}>
            {protocols.map((protocol) => <InlineTag key={protocol} tone="accent">{protocol}</InlineTag>)}
          </div>
        ) : null}

        {capabilities.length ? (
          <div className="model-square-capabilities" aria-label={t("models.squareCapabilities")}>
            {capabilities.map((chip) => <InlineTag key={chip} tone="default">{chip}</InlineTag>)}
          </div>
        ) : null}

        <div className="model-square-channels" aria-label={t("models.squareChannelSupport")}>
          {channels.map((channel) => (
            <span
              key={channel.channel_id}
              className={channel.available ? "model-square-channel model-square-channel-on" : "model-square-channel"}
              title={channelTitle(channel, item.model, t)}
            >
              <span className="model-square-channel-dot" aria-hidden="true" />
              {channel.channel_name || channel.channel_id}
            </span>
          ))}
        </div>
      </Link>

      <footer className="model-square-footer">
        <div className="model-square-links">
          {officialURL ? (
            <a className="model-square-link" href={officialURL} target="_blank" rel="noreferrer">
              <ExternalLink size={13} aria-hidden="true" />
              {t("models.squareOfficial")}
            </a>
          ) : null}
          {modelCardURL ? (
            <a className="model-square-link" href={modelCardURL} target="_blank" rel="noreferrer">
              <ExternalLink size={13} aria-hidden="true" />
              {t("models.squareModelCard")}
            </a>
          ) : null}
          {!facts ? <span className="model-square-unmatched">{t("models.squareUnmatched")}</span> : null}
        </div>
        <Link className="model-square-detail-link" to={detailLink}>
          {t("models.squareDetail")}
          <ArrowUpRight size={13} aria-hidden="true" />
        </Link>
      </footer>
    </article>
  );
}

function SquareMetric({ label, value }) {
  return (
    <span className="model-square-metric">
      <span>{label}</span>
      <strong>{value}</strong>
    </span>
  );
}


function channelProviderLabel(channels, t) {
  const preset = channels.find((channel) => channel.provider_preset)?.provider_preset;
  return preset || t("models.squareConfigured");
}

function channelTitle(channel, model, t) {
  const name = channel.channel_name || channel.channel_id;
  const parts = [name];
  if (channel.channel_name && channel.channel_id && channel.channel_name !== channel.channel_id) {
    parts.push(channel.channel_id);
  }
  parts.push(channelStatusLabel(channel, t));
  if (channel.upstream_model && channel.upstream_model !== model) {
    parts.push(channel.upstream_model);
  }
  return parts.join(" · ");
}

function channelStatusLabel(channel, t) {
  if (!channel.channel_enabled) {
    return t("models.channelDisabled");
  }
  switch (channel.state) {
    case "removed":
      return t("channelDetail.stateRemoved");
    case "disabled":
      return t("channelDetail.stateDisabled");
    default:
      return channel.available ? t("models.squareAvailable") : t("models.squareUnavailable");
  }
}

function providerCount(channels) {
  const providers = new Set();
  for (const channel of channels) {
    if (channel.provider_preset) {
      providers.add(channel.provider_preset);
    }
  }
  return providers.size;
}
