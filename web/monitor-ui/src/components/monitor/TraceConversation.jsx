import React, { useEffect, useMemo, useRef, useState } from "react";
import { ChevronDown, ChevronUp } from "lucide-react";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Card } from "../ui/card";
import { InlineTag } from "../common/Badges";
import { MessageContent } from "../common/Display";
import { EmptyState } from "../common/EmptyState";
import { useI18n } from "../../lib/i18n";
import {
  buildConversationSteps,
  conversationStats,
  conversationText,
  distinctParts,
  findConversationTarget,
  kindLabel,
  nodeKind,
  parseConversationAnchor,
  stepHaystack,
  toolArgumentsOf,
  toolNameOf,
  toolOutputOf,
} from "../../lib/conversation";

/*
 * The 会话 tab.
 *
 * It renders the trace as a conversation - one card per step of the Observation
 * IR, in payload order - instead of as the flat list of extracted messages it
 * used to be. The reason is the audit findings: a finding names a node
 * (`trace#…​#node#node_1bf9…#path#$.input[159]`), and only the IR has addresses
 * a link can land on, so the conversation is built from it and every card
 * carries the node it came from.
 *
 * The shape follows what makes a long trajectory readable: a chronology with a
 * failure marker you can see while scrolling, long text collapsed behind an
 * informative preview rather than a wall, a tool call and its result in one
 * card, and no chrome around the content.
 */

const CALL_KINDS = new Set(["tool_call", "server_tool_call", "code"]);
const RESULT_KINDS = new Set(["tool_result", "server_tool_result", "code_result"]);
const PLAIN_KINDS = new Set(["instruction", "message", "text", "refusal", "safety", "error", "citation"]);

/*
 * The steps the navigation rail marks, and the one the conversation lands on by
 * default: a turn the caller opened. A tool call or its result is part of a
 * turn, not a new one, so it gets no tick.
 */
const INPUT_ROLES = new Set(["user", "human"]);

function isInputStep(step) {
  return INPUT_ROLES.has(String(step.role || "").toLowerCase()) || step.kind === "instruction";
}

function stepKey(step) {
  return step.id || `${step.path}-${step.index}`;
}

function kindTone(kind) {
  if (RESULT_KINDS.has(kind)) {
    return "green";
  }
  if (CALL_KINDS.has(kind)) {
    return "gold";
  }
  if (kind === "reasoning") {
    return "accent";
  }
  if (kind === "error" || kind === "refusal") {
    return "danger";
  }
  return "";
}

function stepTitle(step) {
  if (CALL_KINDS.has(step.kind)) {
    return toolNameOf(step) || toolArgumentsOf(step);
  }
  if (RESULT_KINDS.has(step.kind)) {
    return toolOutputOf(step).text;
  }
  return step.text;
}

function resultFailed(result) {
  const raw = result?.raw && typeof result.raw === "object" ? result.raw : {};
  const status = String(raw.status || "").toLowerCase();
  if (raw.is_error === true) {
    return true;
  }
  const exit = raw.exit_code;
  if (exit !== undefined && exit !== null && Number(exit) !== 0) {
    return true;
  }
  return ["error", "failed", "failure", "fail", "timeout", "cancelled", "canceled", "aborted", "killed"].includes(status);
}

function previewText(text = "") {
  const flat = String(text).replace(/\s*\n+\s*/g, " ⏎ ");
  return flat.length > 160 ? `${flat.slice(0, 160)}…` : flat;
}

function TextBlock({ bodyID, text, format, renderMarkdown, open, onToggle, highlight = "", t }) {
  const long = text.length > 900 || text.split(/\r\n|\r|\n/).length > 12;
  return (
    <div className="conversation-block" id={bodyID}>
      <MessageContent
        value={text}
        format={format}
        renderMarkdown={renderMarkdown}
        className="conversation-text"
        collapsedLines={12}
        forceExpanded={open}
        highlight={highlight}
      />
      {long ? (
        <button type="button" className="conversation-more" onClick={onToggle}>
          {open ? t("common.hide") : t("common.show")}
          <span className="conversation-dim"> · {t("conversation.chars", { count: text.length })}</span>
        </button>
      ) : null}
    </div>
  );
}

function ConversationResult({ result, open, onToggle, renderMarkdown, highlight = "", t }) {
  const failed = resultFailed(result);
  const { text, structured } = toolOutputOf(result);
  const raw = result.raw && typeof result.raw === "object" ? result.raw : {};
  const exit = raw.exit_code;
  return (
    <div className="conversation-result" data-failed={failed ? "true" : "false"}>
      {/* The result is its own node, so it keeps its own anchor inside the card
          it was merged into. */}
      <span className="conversation-anchor" id={result.id} aria-hidden="true" />
      <div className="conversation-result-head">
        <span className="conversation-dim mono">{t("conversation.result")}</span>
        {raw.status ? <InlineTag tone={failed ? "danger" : ""}>{String(raw.status)}</InlineTag> : null}
        {exit !== undefined && exit !== null ? <InlineTag tone={Number(exit) === 0 ? "green" : "danger"}>exit {String(exit)}</InlineTag> : null}
        {failed ? <InlineTag tone="danger">{t("conversation.failed")}</InlineTag> : null}
        <span className="conversation-dim mono">{result.path}</span>
      </div>
      {text ? (
        structured ? (
          <pre className="conversation-pre">{text}</pre>
        ) : (
          <TextBlock
            text={text}
            format="text"
            renderMarkdown={false}
            open={open}
            onToggle={onToggle}
            highlight={highlight}
            t={t}
          />
        )
      ) : (
        <p className="conversation-empty">{t("conversation.noContent")}</p>
      )}
    </div>
  );
}

// A tool call that arrived inside a message - every OpenAI chat trace records
// one this way - drawn as the action it is: what ran, with what arguments, and
// the result that came back.
function ConversationCall({ call, open, onToggle, renderMarkdown, highlight = "", t }) {
  const failed = call.failed || (call.results || []).some(resultFailed);
  const args = toolArgumentsOf(call);
  const raw = call.raw && typeof call.raw === "object" ? call.raw : {};
  return (
    <div className="conversation-call" data-conversation-call="" data-kind={call.kind} data-failed={failed ? "true" : "false"}>
      <span className="conversation-anchor" id={call.id} aria-hidden="true" />
      <div className="conversation-call-head">
        <InlineTag tone={kindTone(call.kind)}>{kindLabel(call.kind, t)}</InlineTag>
        {toolNameOf(call) ? <span className="conversation-name mono">{toolNameOf(call)}</span> : null}
        {failed ? <InlineTag tone="danger">{t("conversation.failed")}</InlineTag> : null}
        <span className="conversation-dim mono">{call.path}</span>
      </div>
      <div className="conversation-call-meta">
        <span className="conversation-dim mono">{t("conversation.callID")}</span>
        <span className="mono">{String((raw.call_id || raw.id || "") || "-")}</span>
      </div>
      {args ? (
        <TextBlock
          text={args}
          format="text"
          renderMarkdown={false}
          open={open}
          onToggle={() => onToggle(call.id)}
          highlight={highlight}
          t={t}
        />
      ) : (
        <p className="conversation-empty">{t("conversation.noArguments")}</p>
      )}
      {(call.parts || []).map((part) =>
        part.text ? (
          <div className="conversation-part" key={part.id}>
            <span className="conversation-anchor" id={part.id} aria-hidden="true" />
            <TextBlock text={part.text} format="text" renderMarkdown={renderMarkdown} open={open} onToggle={() => onToggle(part.id)} highlight={highlight} t={t} />
          </div>
        ) : null,
      )}
      {(call.results || []).map((result) => (
        <ConversationResult key={result.id} result={result} open={open} onToggle={() => onToggle(result.id)} renderMarkdown={renderMarkdown} highlight={highlight} t={t} />
      ))}
    </div>
  );
}

function ConversationStep({ step, ordinal, focused, open, onToggleOpen, onSelectAnchor, renderMarkdown, highlight = "", registerRef, t }) {
  const failed = step.failed || step.results.some(resultFailed);
  const kind = step.kind;
  const isCall = CALL_KINDS.has(kind);
  const isResult = RESULT_KINDS.has(kind);
  const title = stepTitle(step);
  const parts = distinctParts(step);
  const bodyText = isCall ? toolArgumentsOf(step) : isResult ? "" : step.text || parts.map((part) => part.text).filter(Boolean).join("\n\n");
  const extraParts = isCall || isResult ? parts : parts.filter((part) => part.text !== bodyText);
  const format = renderMarkdown && PLAIN_KINDS.has(kind) ? "markdown" : "text";
  const anchorValue = step.id || step.path;

  return (
    <article
      className="conversation-step"
      id={step.id}
      ref={registerRef}
      data-conversation-step=""
      data-kind={kind}
      data-failed={failed ? "true" : "false"}
      data-focused={focused ? "true" : "false"}
      data-node-path={step.path}
    >
      {/* Parts that add nothing textual - or that repeat their parent - still
          need an address, because a finding may name the part. */}
      {step.parts
        .filter((part) => !extraParts.includes(part))
        .map((part) => (
          <span key={part.id} className="conversation-anchor" id={part.id} aria-hidden="true" data-part-path={part.path} />
        ))}
      {(step.calls || [])
        .filter((call) => !call.id)
        .map((call) => (
          <span key={call.path} className="conversation-anchor" aria-hidden="true" data-part-path={call.path} />
        ))}

      <header className="conversation-step-head">
        <span className="conversation-index mono">#{ordinal}</span>
        <InlineTag tone={kindTone(kind)}>{kindLabel(kind, t)}</InlineTag>
        {step.role ? <InlineTag tone="accent">{step.role}</InlineTag> : null}
        {isCall && title ? <span className="conversation-name mono">{title}</span> : null}
        {failed ? <InlineTag tone="danger">{t("conversation.failed")}</InlineTag> : null}
        <span className="conversation-head-spacer" />
        {step.path ? (
          <button
            type="button"
            className="conversation-anchor-chip mono"
            title={t("conversation.locate", { anchor: step.path })}
            onClick={() => onSelectAnchor(step.path)}
          >
            {step.path}
          </button>
        ) : null}
        {step.id ? (
          <button
            type="button"
            className="conversation-anchor-chip mono"
            title={t("conversation.locate", { anchor: step.id })}
            onClick={() => onSelectAnchor(step.id)}
          >
            {step.id.replace(/^node_/, "")}
          </button>
        ) : null}
        {anchorValue ? (
          <Button variant="ghost" size="sm" onClick={() => onSelectAnchor(anchorValue)}>
            {t("conversation.link")}
          </Button>
        ) : null}
      </header>

      <div className="conversation-step-body">
        {isCall ? (
          <>
            <div className="conversation-call-head">
              <span className="conversation-dim mono">{t("conversation.callID")}</span>
              <span className="mono">{String((step.raw && typeof step.raw === "object" ? step.raw.call_id || step.raw.id : "") || "-")}</span>
            </div>
            {bodyText ? (
              <TextBlock
                text={bodyText}
                format="text"
                renderMarkdown={false}
                open={open}
                onToggle={() => onToggleOpen(step.id)}
                highlight={highlight}
                t={t}
              />
            ) : (
              <p className="conversation-empty">{t("conversation.noArguments")}</p>
            )}
          </>
        ) : isResult ? null : bodyText ? (
          <TextBlock
            text={bodyText}
            format={format}
            renderMarkdown={renderMarkdown}
            open={open}
            onToggle={() => onToggleOpen(step.id)}
            highlight={highlight}
            t={t}
          />
        ) : step.calls.length ? null : (
          <pre className="conversation-pre conversation-pre-raw">
            {step.raw ? JSON.stringify(step.raw, null, 2) : t("conversation.noContent")}
          </pre>
        )}

        {step.calls.map((call) => (
          <ConversationCall
            key={call.id}
            call={call}
            open={open}
            onToggle={onToggleOpen}
            renderMarkdown={renderMarkdown}
            highlight={highlight}
            t={t}
          />
        ))}

        {extraParts.map((part) => (
          <div className="conversation-part" key={part.id}>
            <span className="conversation-anchor" id={part.id} aria-hidden="true" />
            <div className="conversation-part-head">
              <InlineTag tone={kindTone(part.kind)}>{kindLabel(part.kind, t)}</InlineTag>
              <span className="conversation-dim mono">{part.path}</span>
            </div>
            {part.text ? (
              <TextBlock
                text={part.text}
                format={renderMarkdown && PLAIN_KINDS.has(part.kind) ? "markdown" : "text"}
                renderMarkdown={renderMarkdown}
                open={open}
                onToggle={() => onToggleOpen(part.id)}
                highlight={highlight}
                t={t}
              />
            ) : part.raw ? (
              <pre className="conversation-pre conversation-pre-raw">{JSON.stringify(part.raw, null, 2)}</pre>
            ) : null}
          </div>
        ))}

        {step.results.map((result) => (
          <ConversationResult
            key={result.id}
            result={result}
            open={open}
            onToggle={() => onToggleOpen(result.id)}
            renderMarkdown={renderMarkdown}
            highlight={highlight}
            t={t}
          />
        ))}
      </div>
    </article>
  );
}

export function TraceConversation({ observation, anchor = "", highlight = "", renderMarkdown, onRenderMarkdownChange, onSelectAnchor, fallback = null }) {
  const { t } = useI18n();
  const steps = useMemo(() => buildConversationSteps(observation?.nodes || []), [observation]);
  const stats = useMemo(() => conversationStats(steps), [steps]);
  const [query, setQuery] = useState("");
  const [failuresOnly, setFailuresOnly] = useState(false);
  const [openAll, setOpenAll] = useState(null);
  const [overrides, setOverrides] = useState({});
  const [landedInput, setLandedInput] = useState("");
  const [railTicks, setRailTicks] = useState([]);
  const scrolled = useRef("");
  const listRef = useRef(null);
  const stepRefs = useRef(new Map());
  const landed = useRef(false);
  const target = useMemo(() => findConversationTarget(steps, anchor), [steps, anchor]);
  const inputSteps = useMemo(
    () => steps.map((step, index) => ({ step, ordinal: index + 1 })).filter(({ step }) => isInputStep(step)),
    [steps],
  );
  // A reader who scrolls while the anchor is still settling keeps their place:
  // the repeats above only belong to the reader who has not moved.
  // A reader who scrolls - or who clicks anything, including the rail - while a
  // landing is still settling keeps their place: the deferred repeats below only
  // belong to the reader who has not moved.
  const settled = useRef(() => false);
  useEffect(() => {
    let moved = false;
    const mark = () => {
      moved = true;
    };
    settled.current = () => moved;
    window.addEventListener("wheel", mark, { passive: true });
    window.addEventListener("touchmove", mark, { passive: true });
    window.addEventListener("pointerdown", mark, { passive: true });
    window.addEventListener("keydown", mark);
    return () => {
      window.removeEventListener("wheel", mark);
      window.removeEventListener("touchmove", mark);
      window.removeEventListener("pointerdown", mark);
      window.removeEventListener("keydown", mark);
    };
  }, []);

  const isOpen = (key, fallback) => {
    if (Object.prototype.hasOwnProperty.call(overrides, key)) {
      return overrides[key];
    }
    if (openAll !== null) {
      return openAll;
    }
    return fallback;
  };
  const toggleOpen = (key) => setOverrides((current) => ({ ...current, [key]: !isOpen(key, false) }));

  // Landing on a finding's anchor is the whole point of the addresses: scroll
  // the step into view once, and leave the URL alone (the reader may scroll
  // away without losing the target they came for).
  useEffect(() => {
    if (!target || scrolled.current === target) {
      return;
    }
    const { path } = parseConversationAnchor(anchor);
    const element =
      document.getElementById(target) ||
      (path ? document.querySelector(`[data-node-path="${path.replaceAll('"', '\\"')}"]`) : null);
    if (!element) {
      return;
    }
    scrolled.current = target;
    // Twice, because the content above the target grows after the first scroll:
    // Markdown is a lazy chunk, so a long message that rendered as plain text
    // re-renders taller once the parser arrives, and a target that was centred
    // ends up below the fold again. The repeats stop as soon as the reader
    // scrolls for themselves, and the last one is a no-op when nothing moved.
    const scrolls = [0, 250, 700, 1500].map((delay) =>
      window.setTimeout(() => {
        if (!settled.current()) {
          element.scrollIntoView({ block: "center", behavior: delay === 0 ? "auto" : "smooth" });
        }
      }, delay),
    );
    return () => {
      scrolls.forEach((timer) => window.clearTimeout(timer));
    };
  }, [target, anchor]);

  // The conversation is long by nature and the reader usually arrives to read the
  // newest turn. Landing on the last input step puts that turn - and the output
  // that answered it - on screen; a finding's anchor still wins, and a reader who
  // is already filtering keeps the view they asked for.
  useEffect(() => {
    if (landed.current || !steps.length) {
      return undefined;
    }
    landed.current = true;
    if (target || query.trim() || failuresOnly) {
      return undefined;
    }
    const lastInput = [...inputSteps].reverse()[0];
    const key = lastInput ? stepKey(lastInput.step) : "";
    const element = key ? stepRefs.current.get(key) : null;
    if (!element) {
      return undefined;
    }
    setLandedInput(key);
    // The same deferred repeats as an anchor landing: a lazy Markdown chunk
    // makes the content above grow after the first scroll.
    const scrolls = [0, 250, 700].map((delay) =>
      window.setTimeout(() => {
        if (!settled.current()) {
          element.scrollIntoView({ block: "start", behavior: delay === 0 ? "auto" : "smooth" });
        }
      }, delay),
    );
    return () => {
      scrolls.forEach((timer) => window.clearTimeout(timer));
    };
  }, [steps, inputSteps, target, query, failuresOnly]);

  // One tick per input turn at its real position in the transcript, so the rail
  // is a minimap of the conversation rather than an evenly-spaced list of
  // numbers. The list is observed because expanding a step moves every tick
  // below it.
  useEffect(() => {
    const list = listRef.current;
    if (!list) {
      return undefined;
    }
    const measure = () => {
      const listTop = list.getBoundingClientRect().top;
      const height = list.offsetHeight || 1;
      const next = [];
      for (const { step, ordinal } of inputSteps) {
        const element = stepRefs.current.get(stepKey(step));
        if (!element) {
          continue;
        }
        const offset = element.getBoundingClientRect().top - listTop;
        next.push({
          key: stepKey(step),
          ordinal,
          percent: Math.min(100, Math.max(0, (offset / height) * 100)),
          preview: previewText(conversationText(step)),
        });
      }
      setRailTicks(next);
    };
    measure();
    const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(measure);
    observer?.observe(list);
    window.addEventListener("resize", measure);
    return () => {
      observer?.disconnect();
      window.removeEventListener("resize", measure);
    };
  }, [inputSteps, query, failuresOnly]);

  if (!steps.length) {
    return fallback;
  }

  const needle = query.trim().toLowerCase();
  const visible = steps.filter((step) => {
    if (failuresOnly && !(step.failed || step.results.some(resultFailed))) {
      return false;
    }
    if (needle && !stepHaystack(step).includes(needle)) {
      return false;
    }
    return true;
  });

  const registerStepRef = (key) => (element) => {
    if (element) {
      stepRefs.current.set(key, element);
    } else {
      stepRefs.current.delete(key);
    }
  };
  const jumpTo = (key) => {
    stepRefs.current.get(key)?.scrollIntoView({ block: "start", behavior: "smooth" });
  };
  const jumpTop = () => {
    stepRefs.current.get(stepKey(steps[0]))?.scrollIntoView({ block: "start", behavior: "smooth" });
  };
  const jumpBottom = () => {
    stepRefs.current.get(stepKey(steps[steps.length - 1]))?.scrollIntoView({ block: "end", behavior: "smooth" });
  };
  const railVisible = steps.length > 1;

  return (
    // The rail is a grid column of its own rather than a floating element over
    // the panel: it reserves the width it needs, so it can never cover the text
    // it navigates, and `position: sticky` keeps it on screen while the page
    // scrolls past a long transcript.
    <div className={railVisible ? "conversation-layout conversation-layout-railed" : "conversation-layout"}>
      <Card as="section" className="conversation-panel">
        <div className="conversation-toolbar">
          <Input
            type="search"
            className="conversation-search"
            value={query}
            placeholder={t("conversation.search")}
            aria-label={t("conversation.search")}
            onChange={(event) => setQuery(event.target.value)}
          />
          <label className="wrap-toggle">
            <input type="checkbox" checked={failuresOnly} onChange={(event) => setFailuresOnly(event.target.checked)} />
            {t("conversation.failuresOnly")}
          </label>
          <label className="wrap-toggle">
            <input type="checkbox" checked={renderMarkdown} onChange={(event) => onRenderMarkdownChange(event.target.checked)} />
            {t("traceDetail.renderMarkdown")}
          </label>
          <Button variant="ghost" size="sm" onClick={() => { setOverrides({}); setOpenAll(openAll === true ? false : true); }}>
            {openAll === true ? t("conversation.collapseAll") : t("conversation.expandAll")}
          </Button>
          {/*
            The rail's two arrows are the desktop way through a long transcript,
            and the rail is hidden on a phone - which left the only two controls
            that answer "where does this start" and "where did it end up" gone
            exactly where scrolling is longest. These are the same two jumps in
            the toolbar; CSS shows them only when the rail is not there.
          */}
          {railVisible ? (
            <>
              <Button variant="ghost" size="sm" className="conversation-jump" title={t("conversation.jumpTop")} aria-label={t("conversation.jumpTop")} onClick={jumpTop}>
                <ChevronUp size={14} aria-hidden="true" />
                {t("conversation.jumpTopShort")}
              </Button>
              <Button variant="ghost" size="sm" className="conversation-jump" title={t("conversation.jumpBottom")} aria-label={t("conversation.jumpBottom")} onClick={jumpBottom}>
                <ChevronDown size={14} aria-hidden="true" />
                {t("conversation.jumpLatestShort")}
              </Button>
            </>
          ) : null}
          <span className="conversation-count mono">
            {t("conversation.count", { shown: visible.length, total: steps.length })}
          </span>
        </div>

        <div className="conversation-summary">
          <InlineTag>{t("conversation.statSteps", { count: stats.steps })}</InlineTag>
          {stats.toolCalls ? <InlineTag tone="gold">{t("conversation.statCalls", { count: stats.toolCalls })}</InlineTag> : null}
          {stats.toolResults ? <InlineTag tone="green">{t("conversation.statResults", { count: stats.toolResults })}</InlineTag> : null}
          {stats.reasoning ? <InlineTag tone="accent">{t("conversation.statReasoning", { count: stats.reasoning })}</InlineTag> : null}
          {stats.failed ? <InlineTag tone="danger">{t("conversation.statFailed", { count: stats.failed })}</InlineTag> : null}
        </div>

        {visible.length ? (
          // The ordinal each card shows is the step's place in the whole
          // conversation, not in the filtered view: a search must not renumber the
          // trace a reader is looking at.
          <div className="conversation-list" ref={listRef}>
            {visible.map((step) => (
              <ConversationStep
                key={stepKey(step)}
                step={step}
                ordinal={steps.indexOf(step) + 1}
                // The mark lasts as long as the URL names this step, because the
                // URL is what a shared finding link carries; the ring it draws is
                // an animation, so the page does not look like something is
                // selected after the reader has moved on.
                // With a matched value the exact bytes carry the location; the
                // whole-card ring would read as if the entire message matched.
                focused={step.id === target && !highlight}
                // A step the URL points at opens: a clamped block would hide the
                // sentence the finding was about. The step the page landed on by
                // default opens too, because the newest input is what the reader
                // came to read.
                open={isOpen(step.id, false) || step.id === target || stepKey(step) === landedInput}
                onToggleOpen={toggleOpen}
                onSelectAnchor={onSelectAnchor}
                renderMarkdown={renderMarkdown}
                highlight={highlight}
                registerRef={registerStepRef(stepKey(step))}
                t={t}
              />
            ))}
          </div>
        ) : (
          <EmptyState title={t("conversation.noMatch")} detail={t("conversation.noMatchDetail")} compact />
        )}
      </Card>
      {railVisible ? (
        <nav className="conversation-rail" aria-label={t("conversation.railLabel")}>
          <button type="button" className="conversation-rail-arrow" onClick={jumpTop} title={t("conversation.jumpTop")} aria-label={t("conversation.jumpTop")}>
            <ChevronUp size={14} aria-hidden="true" />
          </button>
          <div className="conversation-rail-track">
            {railTicks.map((tick) => (
              <button
                key={tick.key}
                type="button"
                className="conversation-rail-tick"
                style={{ top: `${tick.percent}%` }}
                title={`#${tick.ordinal} · ${tick.preview}`}
                aria-label={t("conversation.railTickLabel", { index: tick.ordinal, preview: tick.preview })}
                onClick={() => jumpTo(tick.key)}
              />
            ))}
          </div>
          <button type="button" className="conversation-rail-arrow" onClick={jumpBottom} title={t("conversation.jumpBottom")} aria-label={t("conversation.jumpBottom")}>
            <ChevronDown size={14} aria-hidden="true" />
          </button>
        </nav>
      ) : null}
    </div>
  );
}
