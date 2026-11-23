import { formatDuration, formatTokenCount } from "./monitor.js";

/*
 * Pure helpers behind the trace page's 统计 (statistics) and 轨迹 (trajectory)
 * views.
 *
 * The server owns the measurement; this module only decides how the numbers are
 * laid out. Keeping it pure is what makes the columns testable without a DOM:
 * `tests-unit/sessionSteps.test.js` exercises the same functions the table
 * calls.
 *
 * Two rules the server already enforces and this file must not undo:
 *
 *  - `ttft_ms` is null for a non-streamed exchange, so a missing value is
 *    "—" and never 0;
 *  - the cache ratio is token-weighted across a turn, never the average of the
 *    per-step ratios.
 */

const EMPTY = "—";

/** A token count, or an em dash when the value is not a number. */
export function formatTokens(value) {
  if (value === null || value === undefined) return EMPTY;
  return formatTokenCount(Number(value));
}

/** A duration, or an em dash when the step did not measure one. */
export function formatMillis(value) {
  if (value === null || value === undefined) return EMPTY;
  return formatDuration(Number(value));
}

/** A ratio in [0,1] as a percentage; null means the ratio is not derivable. */
export function formatRatioPercent(value) {
  if (value === null || value === undefined) return EMPTY;
  const ratio = Number(value);
  if (!Number.isFinite(ratio)) return EMPTY;
  return `${(ratio * 100).toFixed(ratio > 0 && ratio < 0.001 ? 2 : 1)}%`;
}

/**
 * Normalize the provider's own usage counters onto disjoint buckets.
 *
 * The proxy keeps the provider's counters apart because they are billed
 * differently, and the two families disagree about what "input" means: OpenAI's
 * input already includes the cache read, Anthropic's does not and reports the
 * cache write separately. Without this step the context total for an
 * OpenAI-family exchange would count the cached tokens twice.
 */
export function observedUsage(usage, { anthropic = false } = {}) {
  const number = (value) => (Number.isFinite(Number(value)) ? Number(value) : 0);
  const cacheRead = number(usage?.cache_read_tokens);
  const cacheWrite = anthropic ? number(usage?.cache_creation_tokens) : 0;
  const input = anthropic
    ? number(usage?.input_tokens)
    : Math.max(0, number(usage?.input_tokens) - cacheRead);
  const context = input + cacheRead + cacheWrite;
  return {
    input,
    cacheRead,
    cacheWrite,
    context,
    output: number(usage?.output_tokens),
    reasoning: number(usage?.reasoning_tokens),
    cacheHit: context > 0 ? Math.min(1, cacheRead / context) : null,
  };
}

/**
 * Whether an exchange speaks Anthropic's usage shape.
 *
 * The endpoint is authoritative when it is known, because a relay channel can
 * record any provider name while still serving /v1/messages; the recorded
 * operation is the next best signal and is what the observation summary always
 * carries.
 */
export function isAnthropicExchange({ provider = "", endpoint = "", operation = "" } = {}) {
  if (String(provider).trim().toLowerCase() === "anthropic") return true;
  if (String(operation).trim().toLowerCase() === "messages") return true;
  return String(endpoint).replace(/\/+$/, "") === "/v1/messages";
}

/** Throughput, or an em dash when the generation span is unknown. */
export function formatThroughput(value) {
  if (value === null || value === undefined) return EMPTY;
  const rate = Number(value);
  if (!Number.isFinite(rate)) return EMPTY;
  return `${rate.toFixed(1)} tok/s`;
}

/**
 * Generated tokens per second over the generation span.
 *
 * The span is derived (duration minus first byte), so a buffered stream leaves
 * a few milliseconds and the ratio explodes: 189 tokens "in 34 ms" is 5,559
 * tok/s, which no model produces. Such values are reported as null so the
 * column shows "—" instead of a confident wrong number. This mirrors the
 * server's `tokensPerSecond`.
 */
export function throughput(output, decodeMs, totalMs) {
  if (!output || decodeMs === null || decodeMs === undefined) return null;
  if (decodeMs < 50) return null;
  if (totalMs > 0 && decodeMs * 10 < totalMs) return null;
  return output / (decodeMs / 1000);
}

/**
 * A turn's stable identity: its first step's trace id.
 *
 * Turn numbers are ordinals within the loaded window, so they shift when older
 * history is prepended. Everything keyed by a turn (expansion state, ledger row
 * keys, React keys) uses this instead of the number.
 */
export function turnKey(turn) {
  return turn?.key || turn?.steps?.[0]?.trace_id || `turn-${turn?.index ?? 0}`;
}

/** The absolute step numbers a turn covers, for disambiguating paged windows. */
export function turnRange(turn) {
  const steps = Array.isArray(turn?.steps) ? turn.steps : [];
  if (!steps.length) return null;
  const first = steps[0].global_index;
  const last = steps[steps.length - 1].global_index;
  if (!first || !last) return null;
  return { first, last };
}

/**
 * Merge page views into one window.
 *
 * Turns are renumbered across the merged list and carry a stable `key` (the
 * trace id of their first step), because the numbers shift as history is
 * loaded: a reader who expanded "第 3 轮" must keep that turn open when "第 1 轮"
 * becomes the newly loaded page's first turn.
 */
export function mergeViews(views) {
  if (!views.length) return null;
  const firstView = views[0];
  const lastView = views[views.length - 1];
  let index = 0;
  const turns = [];
  for (const page of views) {
    for (const turn of page?.turns || []) {
      index += 1;
      // The steps are copied because the merge annotates them: mutating the
      // page objects would edit the query cache the next render reads.
      turns.push({ ...turn, index, key: turnKey(turn), steps: (turn.steps || []).map((step) => ({ ...step })) });
    }
  }
  fillBoundaryToolWaits(turns);
  const totals = { ...(lastView.totals || {}) };
  const window = {
    returned: turns.reduce((count, turn) => count + (turn.steps?.length || 0), 0),
    first_step: firstView.window?.first_step,
    last_step: lastView.window?.last_step,
    has_before: Boolean(firstView.window?.has_before),
    has_after: Boolean(lastView.window?.has_after),
    content_pending: views.reduce((count, page) => count + (page.window?.content_pending || 0), 0),
    budget_ms: lastView.window?.budget_ms,
    pages: views.length,
  };
  // The server only reports a turn count when its own window covered the whole
  // session; the merged window knows better once both ends are closed.
  if (!window.has_before && !window.has_after) totals.turns = turns.length;
  return {
    session_id: lastView.session_id,
    focus_trace_id: lastView.focus_trace_id,
    builder_version: lastView.builder_version,
    window,
    totals,
    turns,
    warnings: lastView.warnings || [],
  };
}

/** Whether a step counts as failed for the turn badge. */
export function stepFailed(step) {
  const status = Number(step?.status || 0);
  if (status === 0) return false; // not recorded, not a failure
  return status < 200 || status >= 300;
}

/**
 * Aggregate one turn's steps.
 *
 * `context` sums every step; `latestContext` is the last measured prompt size,
 * which is what a reader means by "how big is the context now". The table's
 * context-mode switch picks between them.
 */
export function turnAggregate(turn) {
  const steps = Array.isArray(turn?.steps) ? turn.steps : [];
  let input = 0;
  let cacheRead = 0;
  let cacheWrite = 0;
  let context = 0;
  let output = 0;
  let latestContext = null;
  let ttft = 0;
  let ttftSteps = 0;
  let model = 0;
  let toolWait = 0;
  let toolSteps = 0;
  let calls = 0;
  let failures = 0;
  let answerChars = 0;
  for (const step of steps) {
    const usage = step?.usage || {};
    input += Number(usage.input || 0);
    cacheRead += Number(usage.cache_read || 0);
    cacheWrite += Number(usage.cache_write || 0);
    context += Number(usage.context || 0);
    output += Number(usage.output || 0);
    if (Number(usage.context || 0) > 0) latestContext = Number(usage.context);
    const timing = step?.timing || {};
    if (timing.ttft_ms !== null && timing.ttft_ms !== undefined) {
      ttft += Number(timing.ttft_ms);
      ttftSteps += 1;
    }
    model += Number(timing.model_ms || 0);
    if (timing.tool_wait_ms !== null && timing.tool_wait_ms !== undefined) {
      toolWait += Number(timing.tool_wait_ms);
      toolSteps += 1;
    }
    calls += Number(step?.did?.calls || 0);
    answerChars += Number(step?.did?.answer_chars || 0);
    if (stepFailed(step)) failures += 1;
  }
  const decodeMs = Math.max(0, model - ttft);
  return {
    input,
    cacheRead,
    cacheWrite,
    context,
    latestContext,
    output,
    cacheHit: context > 0 ? Math.min(1, cacheRead / context) : null,
    ttftMs: ttftSteps > 0 ? ttft : null,
    ttftSteps,
    modelMs: model,
    decodeMs: ttftSteps > 0 && decodeMs > 0 ? decodeMs : null,
    tps: throughput(output, ttftSteps > 0 ? decodeMs : null, model),
    toolWaitMs: toolSteps > 0 ? toolWait : null,
    wallMs: Number(turn?.wall_ms || 0),
    calls,
    failures,
    answerChars,
    steps: steps.length,
  };
}

/** Aggregate a list of turns (the "本窗口合计" row). */
export function windowAggregate(turns) {
  const list = Array.isArray(turns) ? turns : [];
  const totals = {
    steps: 0,
    context: 0,
    latestContext: null,
    output: 0,
    cacheRead: 0,
    input: 0,
    cacheWrite: 0,
    ttftMs: null,
    modelMs: 0,
    toolWaitMs: null,
    wallMs: 0,
    calls: 0,
    failures: 0,
  };
  let ttft = 0;
  let ttftSeen = false;
  let tool = 0;
  let toolSeen = false;
  for (const turn of list) {
    const aggregate = turnAggregate(turn);
    totals.steps += aggregate.steps;
    totals.context += aggregate.context;
    totals.output += aggregate.output;
    totals.cacheRead += aggregate.cacheRead;
    totals.input += aggregate.input;
    totals.cacheWrite += aggregate.cacheWrite;
    totals.modelMs += aggregate.modelMs;
    totals.calls += aggregate.calls;
    totals.failures += aggregate.failures;
    totals.wallMs += aggregate.wallMs;
    if (aggregate.latestContext !== null) totals.latestContext = aggregate.latestContext;
    if (aggregate.ttftMs !== null) {
      ttft += aggregate.ttftMs;
      ttftSeen = true;
    }
    if (aggregate.toolWaitMs !== null) {
      tool += aggregate.toolWaitMs;
      toolSeen = true;
    }
  }
  totals.ttftMs = ttftSeen ? ttft : null;
  totals.toolWaitMs = toolSeen ? tool : null;
  totals.cacheHit = totals.context > 0 ? Math.min(1, totals.cacheRead / totals.context) : null;
  const decode = totals.ttftMs === null ? 0 : Math.max(0, totals.modelMs - totals.ttftMs);
  totals.tps = throughput(totals.output, totals.ttftMs !== null ? decode : null, totals.modelMs);
  return totals;
}

/**
 * The four segments of the total-time bar. "其余" is whatever the model and the
 * client did not account for, and is clamped because the recorded wall is the
 * authority.
 */
export function timeBarSegments({ wallMs, modelMs, ttftMs, toolWaitMs, toolTotalMs }) {
  const wall = Math.max(0, Number(wallMs || 0));
  if (wall <= 0) return { wall, segments: [], widthPercent: 0 };
  const ttft = Math.min(wall, Math.max(0, Number(ttftMs ?? 0)));
  const model = Math.min(wall, Math.max(0, Number(modelMs ?? 0)));
  const decode = Math.max(0, model - ttft);
  const toolSource = toolTotalMs !== null && toolTotalMs !== undefined ? Number(toolTotalMs) : Number(toolWaitMs ?? 0);
  const tool = Math.min(Math.max(0, wall - model), Math.max(0, toolSource));
  const rest = Math.max(0, wall - model - tool);
  return {
    wall,
    widthPercent: 100,
    segments: [
      { key: "ttft", ms: ttft },
      { key: "decode", ms: decode },
      { key: "tool", ms: tool },
      { key: "rest", ms: rest },
    ].filter((segment) => segment.ms > 0),
  };
}

/**
 * Bar width uses a square-root scale, so a ten-minute turn does not flatten a
 * ten-second one into an invisible sliver while the ordering stays honest.
 * Steps are compared within their turn and turns within the window, which is
 * why the scale is passed in rather than derived here.
 */
export function barWidthPercent(value, scale) {
  const top = Number(scale || 0);
  if (!Number.isFinite(top) || top <= 0) return 0;
  const ratio = Math.min(1, Math.max(0, Number(value || 0) / top));
  return Math.round(Math.sqrt(ratio) * 100);
}

/** The largest wall clock among the given turns, used as the turn bar scale. */
export function turnScale(turns) {
  let scale = 0;
  for (const turn of Array.isArray(turns) ? turns : []) {
    scale = Math.max(scale, Number(turn?.wall_ms || 0));
  }
  return scale;
}

/** The largest wall clock among one turn's steps, used as the step bar scale. */
export function stepScale(turn) {
  let scale = 0;
  for (const step of Array.isArray(turn?.steps) ? turn.steps : []) {
    scale = Math.max(scale, Number(step?.timing?.wall_ms ?? step?.timing?.model_ms ?? 0));
  }
  return scale;
}

/** The "执行内容" cell: tool names with counts, or a note that none ran. */
export function toolSummary(step) {
  const tools = Array.isArray(step?.did?.tools) ? step.did.tools : [];
  if (!tools.length) return "";
  return tools.map((tool) => (tool.calls > 1 ? `${tool.name} ×${tool.calls}` : tool.name)).join(" · ");
}

/** The context increment against the previous measured step of the same turn. */
export function contextDelta(step, previousStep) {
  const current = Number(step?.usage?.context ?? 0);
  const previous = Number(previousStep?.usage?.context ?? 0);
  if (!current || !previous) return null;
  return current - previous;
}

/**
 * Flatten a view into the ledger's rows.
 *
 * Tool results arrive in the *next* request, so they are indexed by call id
 * across the window and attached to the call that produced them. A result with
 * no matching call (the call is outside the window) is dropped: showing it
 * without its request would read as a step that ran nothing.
 */
export function buildLedgerRows(turns) {
  const results = new Map();
  for (const turn of Array.isArray(turns) ? turns : []) {
    for (const step of Array.isArray(turn?.steps) ? turn.steps : []) {
      for (const result of step?.content?.tool_results || []) {
        if (result?.call_id) results.set(result.call_id, result);
      }
    }
  }
  const rows = [];
  for (const turn of Array.isArray(turns) ? turns : []) {
    rows.push({
      kind: "turn",
      key: `turn-${turnKey(turn)}`,
      turn,
      index: turn.index,
      startedAt: turn.started_at,
      wallMs: turn.wall_ms,
      trigger: turn.trigger || {},
      aggregate: turnAggregate(turn),
    });
    for (const step of Array.isArray(turn?.steps) ? turn.steps : []) {
      rows.push({
        kind: "step",
        key: step.trace_id,
        step,
        turn,
        failed: stepFailed(step),
        tools: toolSummary(step),
      });
      const content = step?.content;
      if (!content) continue;
      if (content.answer) {
        rows.push({ kind: "answer", key: `${step.trace_id}-answer`, step, turn, text: content.answer });
      }
      for (const [index, call] of (content.tool_calls || []).entries()) {
        rows.push({
          kind: "tool",
          key: `${step.trace_id}-call-${index}`,
          step,
          turn,
          call,
          result: call?.id ? results.get(call.id) || null : null,
        });
      }
    }
  }
  return rows;
}

/**
 * Fill the tool wait across a page boundary.
 *
 * Each page measures the gap only between its own steps, so the last step of a
 * page that asked for tools has no successor to subtract and reports nothing.
 * Once two pages are stitched together that successor exists, and the merged
 * view can apply the server's own rule: the gap between the response and the
 * next request. Steps the server already measured are left alone, and a step
 * with no successor stays unknown rather than becoming zero.
 */
export function fillBoundaryToolWaits(turns) {
  const steps = [];
  for (const turn of turns || []) {
    for (const step of turn?.steps || []) steps.push(step);
  }
  for (let index = 0; index + 1 < steps.length; index += 1) {
    const step = steps[index];
    const timing = step?.timing;
    if (!timing || timing.tool_wait_ms !== null && timing.tool_wait_ms !== undefined) continue;
    if (!(Number(step?.did?.calls) > 0)) continue;
    const started = Date.parse(steps[index + 1]?.started_at);
    const ended = Date.parse(step?.ended_at);
    if (!Number.isFinite(started) || !Number.isFinite(ended)) continue;
    timing.tool_wait_ms = Math.max(0, Math.round(started - ended));
  }
  return turns;
}

/**
 * Keep the rows a search matches, plus the turn and step headers they belong
 * to: a matching tool call without its request would read as an orphan.
 * Matching is case-insensitive over the text a row shows, which is what a
 * reader types when they are looking for a trace id, a model or a tool name.
 */
export function filterLedgerRows(rows, query) {
  const needle = String(query || "").trim().toLowerCase();
  if (!needle) return rows;
  const matchedRows = new Set();
  const matchedSteps = new Set();
  const matchedTurns = new Set();
  for (const row of rows) {
    if (row.kind === "turn") continue;
    const haystack = [
      row.text,
      row.tools,
      row.call?.name,
      row.call?.args,
      row.result?.text,
      row.step?.trace_id,
      row.step?.model,
    ]
      .filter(Boolean)
      .join("\n")
      .toLowerCase();
    if (!haystack.includes(needle)) continue;
    matchedRows.add(row.key);
    if (row.step?.trace_id) matchedSteps.add(row.step.trace_id);
    if (row.turn) matchedTurns.add(turnKey(row.turn));
  }
  const out = [];
  for (const row of rows) {
    if (row.kind === "turn") {
      if (matchedTurns.has(turnKey(row.turn))) out.push(row);
      continue;
    }
    if (row.kind === "step") {
      if (matchedSteps.has(row.step?.trace_id)) out.push(row);
      continue;
    }
    if (matchedRows.has(row.key)) out.push(row);
  }
  return out;
}

/** Whether a view carries any inferred (rather than measured) value. */
export function hasEstimates(view) {
  for (const turn of view?.turns || []) {
    for (const step of turn?.steps || []) {
      if (Array.isArray(step?.estimated) && step.estimated.length) return true;
    }
  }
  return false;
}
