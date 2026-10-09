import React, { useMemo, useState } from "react";
import { Card } from "../ui/card";
import { EmptyState } from "../common/EmptyState";
import { InlineTag } from "../common/Badges";
import { MessageContent } from "../common/Display";
import { useI18n } from "../../lib/i18n";

function valueText(value) {
  if (value === null || value === undefined) return "";
  if (typeof value === "string") return value;
  return JSON.stringify(value, null, 2);
}

function SessionStep({ step, index }) {
  const { t } = useI18n();
  const [expanded, setExpanded] = useState(false);
  const message = valueText(step.message);
  const reasoning = valueText(step.reasoning_content);
  const tools = Array.isArray(step.tool_calls) ? step.tool_calls : [];
  const results = Array.isArray(step.observation?.results) ? step.observation.results : [];
  const long = message.length > 900 || reasoning.length > 900;
  return (
    <article className={`session-trajectory-step session-trajectory-${step.source || "unknown"}`}>
      <div className="session-trajectory-marker">{index + 1}</div>
      <div className="session-trajectory-body">
        <header className="session-trajectory-head">
          <div>
            <strong>{step.source || t("sessionDetail.unknownModel")}</strong>
            {step.model_name ? <span className="session-trajectory-model">{step.model_name}</span> : null}
          </div>
          <div className="trace-tag-group">
            {step.reasoning_effort ? <InlineTag tone="accent">{step.reasoning_effort}</InlineTag> : null}
            {step.metrics ? <InlineTag tone="green">{t("sessionDetail.trajectoryTokens", { count: (step.metrics.prompt_tokens || 0) + (step.metrics.completion_tokens || 0) })}</InlineTag> : null}
          </div>
        </header>
        {message ? <MessageContent value={message} format="markdown" renderMarkdown={!long || expanded} className="session-trajectory-message" /> : null}
        {reasoning ? <details className="session-trajectory-reasoning"><summary>{t("sessionDetail.reasoning")}</summary><pre>{reasoning}</pre></details> : null}
        {tools.map((tool, toolIndex) => <div className="session-trajectory-tool" key={`${tool.tool_call_id || tool.function_name}-${toolIndex}`}><InlineTag tone="gold">{tool.function_name || t("conversation.toolCall")}</InlineTag><pre>{JSON.stringify(tool.arguments || {}, null, 2)}</pre></div>)}
        {results.map((result, resultIndex) => <div className="session-trajectory-result" key={`${result.source_call_id || "result"}-${resultIndex}`}><InlineTag tone="green">{t("conversation.result")}</InlineTag><pre>{result.content || ""}</pre></div>)}
        {long ? <button type="button" className="conversation-more" onClick={() => setExpanded((value) => !value)}>{expanded ? t("common.hide") : t("common.show")}</button> : null}
      </div>
    </article>
  );
}

export function SessionTrajectory({ trajectory, loading, error }) {
  const { t } = useI18n();
  const steps = useMemo(() => trajectory?.steps || [], [trajectory]);
  if (loading) return <EmptyState title={t("sessionDetail.loadingTrajectory")} detail={t("sessionDetail.loadingTrajectoryDetail")} />;
  if (error) return <EmptyState title={t("sessionDetail.trajectoryError")} detail={error} tone="danger" />;
  if (!steps.length) return <EmptyState title={t("sessionDetail.noTrajectory")} detail={t("sessionDetail.noTrajectoryDetail")} />;
  return <Card as="section" className="session-trajectory-panel"><div className="panel-head"><div><h2>{t("sessionDetail.trajectory")}</h2><p className="panel-subtitle">{t("sessionDetail.trajectoryDetail", { count: steps.length })}</p></div><InlineTag tone="accent">{trajectory.agent?.name || "agent"}</InlineTag></div><div className="session-trajectory-list">{steps.map((step, index) => <SessionStep key={`${step.step_id}-${index}`} step={step} index={index} />)}</div></Card>;
}
