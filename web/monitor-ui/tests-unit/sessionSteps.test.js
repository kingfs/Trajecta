import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
  barWidthPercent,
  buildLedgerRows,
  filterLedgerRows,
  filterRowsByTraceIDs,
  formatRatioPercent,
  formatThroughput,
  isAnthropicExchange,
  matchedTraceIDs,
  mergeViews,
  observedUsage,
  turnKey,
  throughput,
  timeBarSegments,
  turnAggregate,
  windowAggregate,
} from "../src/lib/sessionSteps.js";

/*
 * The 统计 and 轨迹 tabs compute their columns from these helpers, so the
 * arithmetic that a reader would check by hand is asserted here rather than
 * through the DOM: a turn's cache rate is token-weighted, a missing first-token
 * measurement stays missing, and the time bar never claims more time than the
 * recorded wall clock.
 */

function step(overrides = {}) {
  return {
    trace_id: overrides.trace_id || "t",
    status: overrides.status ?? 200,
    usage: {
      input: 20,
      cache_read: 80,
      cache_write: 0,
      context: 100,
      output: 10,
      cache_hit: 0.8,
      ...overrides.usage,
    },
    timing: {
      ttft_ms: 200,
      model_ms: 1000,
      decode_ms: 800,
      tps: 12.5,
      tool_wait_ms: null,
      wall_ms: 1000,
      ...overrides.timing,
    },
    did: { tools: [], calls: 0, answer_chars: 0, ...overrides.did },
    content: overrides.content,
  };
}

describe("turn aggregation", () => {
  it("weights the cache rate by tokens instead of averaging per-step ratios", () => {
    const turn = {
      wall_ms: 3000,
      steps: [
        step({ usage: { context: 1000, cache_read: 900, output: 10, cache_hit: 0.9 } }),
        step({
          usage: { context: 10, cache_read: 0, output: 5, cache_hit: 0 },
          timing: { ttft_ms: null, model_ms: 500, wall_ms: 500 },
        }),
      ],
    };
    const aggregate = turnAggregate(turn);
    assert.equal(aggregate.context, 1010);
    assert.equal(aggregate.cacheRead, 900);
    assert.equal(aggregate.cacheHit, 900 / 1010);
    assert.equal(aggregate.steps, 2);
    assert.equal(aggregate.latestContext, 10);
  });

  it("keeps a missing first-token value missing and derives throughput only when measured", () => {
    const turn = {
      wall_ms: 1000,
      steps: [
        step({ timing: { ttft_ms: null, model_ms: 1000, wall_ms: 1000, tps: null } }),
        step({ usage: { output: 20 }, timing: { ttft_ms: null, model_ms: 1000, wall_ms: 1000 } }),
      ],
    };
    const aggregate = turnAggregate(turn);
    assert.equal(aggregate.ttftMs, null);
    assert.equal(aggregate.tps, null);
    assert.equal(aggregate.modelMs, 2000);
  });

  it("counts failures and tool calls", () => {
    const turn = {
      wall_ms: 2000,
      steps: [
        step({ status: 500, did: { calls: 2, tools: [{ name: "bash", calls: 2 }] } }),
        step({ status: 200, did: { calls: 0 } }),
      ],
    };
    const aggregate = turnAggregate(turn);
    assert.equal(aggregate.failures, 1);
    assert.equal(aggregate.calls, 2);
  });
});

describe("throughput", () => {
  it("reports nothing when the generation span is too small to measure", () => {
    // A buffered stream: first byte and last byte land together, so the derived
    // span is a few milliseconds and the ratio would be invented.
    assert.equal(throughput(189, 34, 1908), null);
    assert.equal(throughput(189, 88, 1920), null);
  });

  it("reports a rate when the span is a real share of the exchange", () => {
    assert.equal(throughput(800, 800, 1000), 1000);
    assert.equal(throughput(40, 40, 2000), null, "40 ms is below the floor");
  });

  it("keeps a turn aggregate honest", () => {
    const turn = {
      wall_ms: 1908,
      steps: [
        step({
          usage: { output: 189 },
          timing: { ttft_ms: 1874, model_ms: 1908, decode_ms: 34, tps: null, wall_ms: 1908 },
        }),
      ],
    };
    const aggregate = turnAggregate(turn);
    assert.equal(aggregate.ttftMs, 1874);
    assert.equal(aggregate.tps, null);
  });
});

describe("window aggregation", () => {
  it("sums the turns and keeps the token-weighted ratio", () => {
    const turns = [
      { wall_ms: 1000, steps: [step({ usage: { context: 100, cache_read: 50 } })] },
      { wall_ms: 2000, steps: [step({ usage: { context: 300, cache_read: 150 } })] },
    ];
    const totals = windowAggregate(turns);
    assert.equal(totals.steps, 2);
    assert.equal(totals.context, 400);
    assert.equal(totals.cacheRead, 200);
    assert.equal(totals.cacheHit, 0.5);
    assert.equal(totals.wallMs, 3000);
  });
});

describe("the time bar", () => {
  it("splits the wall clock into first token, generation, tool and the rest", () => {
    const { segments, wall } = timeBarSegments({
      wallMs: 10000,
      modelMs: 6000,
      ttftMs: 1000,
      toolWaitMs: 2000,
    });
    assert.equal(wall, 10000);
    const byKey = Object.fromEntries(segments.map((segment) => [segment.key, segment.ms]));
    assert.equal(byKey.ttft, 1000);
    assert.equal(byKey.decode, 5000);
    assert.equal(byKey.tool, 2000);
    assert.equal(byKey.rest, 2000);
    const total = segments.reduce((sum, segment) => sum + segment.ms, 0);
    assert.equal(total, 10000);
  });

  it("never lets the tool segment push the bar past the wall clock", () => {
    const { segments } = timeBarSegments({ wallMs: 1000, modelMs: 1000, ttftMs: 100, toolWaitMs: 5000 });
    const total = segments.reduce((sum, segment) => sum + segment.ms, 0);
    assert.equal(total, 1000);
    assert.equal(segments.find((segment) => segment.key === "tool"), undefined);
  });

  it("has no bar when the wall clock was not recorded", () => {
    const { segments, widthPercent } = timeBarSegments({ wallMs: 0, modelMs: 0 });
    assert.deepEqual(segments, []);
    assert.equal(widthPercent, 0);
  });
});

describe("bar width", () => {
  it("uses a square-root scale so a short step stays visible", () => {
    assert.equal(barWidthPercent(100, 100), 100);
    assert.equal(barWidthPercent(25, 100), 50);
    assert.equal(barWidthPercent(0, 100), 0);
    assert.equal(barWidthPercent(100, 0), 0);
  });
});

describe("ledger rows", () => {
  const turns = [
    {
      index: 1,
      wall_ms: 2000,
      trigger: { kind: "user", text: "hello" },
      steps: [
        {
          trace_id: "a",
          global_index: 1,
          model: "gpt-test",
          usage: { context: 100, output: 5, cache_hit: 0.5 },
          timing: { model_ms: 1000, wall_ms: 1000 },
          did: { tools: [{ name: "bash", calls: 1 }], calls: 1 },
          content: {
            tool_calls: [{ id: "call_1", name: "bash", args: "{\"command\":\"ls\"}" }],
            tool_results: [],
            answer: "",
          },
        },
        {
          trace_id: "b",
          global_index: 2,
          usage: { context: 120, output: 7 },
          timing: { model_ms: 800, wall_ms: 800 },
          did: { tools: [], calls: 0 },
          content: {
            tool_calls: [],
            tool_results: [{ call_id: "call_1", name: "bash", text: "file.txt", chars: 8 }],
            answer: "done",
          },
        },
      ],
    },
  ];

  it("attaches a tool result to the call that produced it", () => {
    const rows = buildLedgerRows(turns);
    const tool = rows.find((row) => row.kind === "tool");
    assert.ok(tool);
    assert.equal(tool.call.name, "bash");
    assert.equal(tool.result.text, "file.txt");
    assert.deepEqual(
      rows.map((row) => row.kind),
      ["turn", "step", "tool", "step", "answer"],
    );
  });

  it("drops a result whose call is outside the window", () => {
    const orphan = [
      {
        index: 1,
        steps: [
          {
            trace_id: "c",
            content: { tool_calls: [], tool_results: [{ call_id: "gone", text: "x" }] },
          },
        ],
      },
    ];
    const rows = buildLedgerRows(orphan);
    assert.equal(rows.some((row) => row.kind === "tool"), false);
  });

  it("keeps the turn header when only a nested row matches", () => {
    const rows = buildLedgerRows(turns);
    const filtered = filterLedgerRows(rows, "file.txt");
    assert.deepEqual(
      filtered.map((row) => row.kind),
      ["turn", "step", "tool"],
    );
  });

  it("keeps only the selected steps and their turn headers", () => {
    const rows = buildLedgerRows(turns);
    const filtered = filterRowsByTraceIDs(rows, new Set(["b"]));
    assert.deepEqual(
      filtered.map((row) => row.kind),
      ["turn", "step", "answer"],
      "the turn header comes back for the step that survived",
    );
    assert.equal(filtered[1].step.trace_id, "b");
    // No selection at all means no filtering; an empty selection means nothing.
    assert.equal(filterRowsByTraceIDs(rows, null).length, rows.length);
    assert.equal(filterRowsByTraceIDs(rows, new Set()).length, 0);
  });

  it("reports the steps whose rows matched, for the timeline highlight", () => {
    const rows = buildLedgerRows(turns);
    assert.deepEqual([...matchedTraceIDs(rows, "bash")], ["a"], "the tool call belongs to the first step");
    assert.equal(matchedTraceIDs(rows, "  "), null, "an empty query highlights nothing");
    assert.equal(matchedTraceIDs(rows, "nothing-matches").size, 0);
  });

  it("matches a model name and keeps everything for an empty query", () => {
    const rows = buildLedgerRows(turns);
    assert.equal(filterLedgerRows(rows, "  ").length, rows.length);
    const filtered = filterLedgerRows(rows, "gpt-test");
    assert.deepEqual(
      filtered.map((row) => row.kind),
      ["turn", "step", "tool"],
      "the tool row inherits the step it belongs to, so it matches alongside that step",
    );
  });
});

describe("merging paged windows", () => {
  function page(turnIds, steps, window) {
    return {
      turns: turnIds.map((id, index) => ({
        index: index + 1,
        steps: [step({ trace_id: id, global_index: steps[index] })],
      })),
      window,
      totals: { steps: 999, context: 10, output: 1, turns: null },
    };
  }

  it("stitches older and newer pages around the first one and renumbers the turns", () => {
    const older = page(["t1", "t2"], [1, 2], { first_step: 1, last_step: 2, has_before: false, has_after: true });
    const first = page(["t3", "t4"], [3, 4], { first_step: 3, last_step: 4, has_before: true, has_after: true });
    const newer = page(["t5"], [5], { first_step: 5, last_step: 5, has_before: true, has_after: false });
    const merged = mergeViews([older, first, newer]);

    assert.deepEqual(merged.turns.map((turn) => turn.index), [1, 2, 3, 4, 5]);
    assert.deepEqual(merged.turns.map(turnKey), ["t1", "t2", "t3", "t4", "t5"]);
    assert.equal(merged.window.first_step, 1);
    assert.equal(merged.window.last_step, 5);
    assert.equal(merged.window.returned, 5);
    assert.equal(merged.window.has_before, false);
    assert.equal(merged.window.has_after, false);
  });

  it("only claims a turn count once both ends of the session are loaded", () => {
    const partial = mergeViews([
      page(["t3"], [3], { first_step: 3, last_step: 3, has_before: true, has_after: true }),
    ]);
    assert.equal(partial.totals.turns, null);
    const whole = mergeViews([
      page(["t3"], [3], { first_step: 3, last_step: 3, has_before: false, has_after: false }),
    ]);
    assert.equal(whole.totals.turns, 1);
  });

  it("keeps a turn's identity stable when older history is prepended", () => {
    const before = mergeViews([page(["t3"], [3], { first_step: 3, last_step: 3, has_before: true, has_after: false })]);
    const after = mergeViews([
      page(["t1"], [1], { first_step: 1, last_step: 1, has_before: false, has_after: true }),
      page(["t3"], [3], { first_step: 3, last_step: 3, has_before: true, has_after: false }),
    ]);
    assert.equal(turnKey(before.turns[0]), "t3");
    assert.equal(turnKey(after.turns[1]), "t3");
    assert.equal(after.turns[1].index, 2, "the ordinal moved, the identity did not");
  });

  it("returns nothing for no pages", () => {
    assert.equal(mergeViews([]), null);
  });
});

describe("formatting", () => {
  it("prints an em dash for a value that was not measured", () => {
    assert.equal(formatRatioPercent(null), "—");
    assert.equal(formatRatioPercent(undefined), "—");
    assert.equal(formatRatioPercent(0.5), "50.0%");
    assert.equal(formatThroughput(null), "—");
    assert.equal(formatThroughput(12.34), "12.3 tok/s");
  });
});

describe("observed usage", () => {
  it("subtracts the cache read from an OpenAI-family input count", () => {
    const usage = observedUsage({ input_tokens: 1000, output_tokens: 20, cache_read_tokens: 900 });
    assert.equal(usage.input, 100);
    assert.equal(usage.cacheRead, 900);
    assert.equal(usage.cacheWrite, 0);
    assert.equal(usage.context, 1000);
    assert.equal(usage.cacheHit, 0.9);
  });

  it("adds Anthropic's cache buckets to its already-exclusive input", () => {
    const usage = observedUsage(
      { input_tokens: 50, output_tokens: 10, cache_read_tokens: 800, cache_creation_tokens: 150 },
      { anthropic: true },
    );
    assert.equal(usage.input, 50);
    assert.equal(usage.cacheWrite, 150);
    assert.equal(usage.context, 1000);
    assert.equal(usage.cacheHit, 0.8);
  });

  it("has no ratio without a prompt", () => {
    assert.equal(observedUsage({ output_tokens: 5 }).cacheHit, null);
    assert.equal(observedUsage({ output_tokens: 5 }).context, 0);
  });

  it("names the Anthropic wire shape by provider, operation or endpoint", () => {
    assert.equal(isAnthropicExchange({ provider: "anthropic", endpoint: "/v1/chat/completions" }), true);
    assert.equal(isAnthropicExchange({ provider: "openai_compatible", endpoint: "/v1/messages/" }), true);
    assert.equal(isAnthropicExchange({ provider: "openai_compatible", operation: "messages" }), true);
    assert.equal(isAnthropicExchange({ provider: "openai_compatible", endpoint: "/v1/chat/completions" }), false);
    assert.equal(isAnthropicExchange(), false);
  });
});

describe("page-boundary tool wait", () => {
  function timedStep(traceID, globalIndex, calls, endedAt) {
    const value = step({
      trace_id: traceID,
      global_index: globalIndex,
      did: { calls, tools: [], answer_chars: 0 },
      timing: { ttft_ms: 200, model_ms: 1000, wall_ms: 1000, tool_wait_ms: null },
    });
    value.started_at = endedAt;
    value.ended_at = endedAt;
    return value;
  }

  it("fills the gap that only exists once the next page is stitched in", () => {
    const first = mergeViews([
      {
        turns: [{ index: 1, steps: [timedStep("t1", 1, 1, "2026-05-01T09:00:00Z")] }],
        window: { first_step: 1, last_step: 1, has_before: false, has_after: true },
        totals: {},
      },
    ]);
    assert.equal(first.turns[0].steps[0].timing.tool_wait_ms, null, "no successor yet");

    const merged = mergeViews([
      {
        turns: [{ index: 1, steps: [timedStep("t1", 1, 1, "2026-05-01T09:00:00Z")] }],
        window: { first_step: 1, last_step: 1, has_before: false, has_after: true },
        totals: {},
      },
      {
        turns: [{ index: 1, steps: [timedStep("t2", 2, 0, "2026-05-01T09:00:03Z")] }],
        window: { first_step: 2, last_step: 2, has_before: true, has_after: false },
        totals: {},
      },
    ]);
    assert.equal(merged.turns[0].steps[0].timing.tool_wait_ms, 3000);
    assert.equal(merged.turns[1].steps[0].timing.tool_wait_ms, null, "the last step stays unknown");
  });

  it("does not invent a wait for a step that called no tools", () => {
    const merged = mergeViews([
      {
        turns: [{ index: 1, steps: [timedStep("t1", 1, 0, "2026-05-01T09:00:00Z")] }],
        window: { first_step: 1, last_step: 1, has_before: false, has_after: true },
        totals: {},
      },
      {
        turns: [{ index: 1, steps: [timedStep("t2", 2, 0, "2026-05-01T09:00:03Z")] }],
        window: { first_step: 2, last_step: 2, has_before: true, has_after: false },
        totals: {},
      },
    ]);
    assert.equal(merged.turns[0].steps[0].timing.tool_wait_ms, null);
  });

  it("leaves a measured value alone", () => {
    const measured = timedStep("t1", 1, 2, "2026-05-01T09:00:00Z");
    measured.timing.tool_wait_ms = 42;
    const merged = mergeViews([
      {
        turns: [{ index: 1, steps: [measured] }],
        window: { first_step: 1, last_step: 1, has_before: false, has_after: true },
        totals: {},
      },
      {
        turns: [{ index: 1, steps: [timedStep("t2", 2, 0, "2026-05-01T09:00:03Z")] }],
        window: { first_step: 2, last_step: 2, has_before: true, has_after: false },
        totals: {},
      },
    ]);
    assert.equal(merged.turns[0].steps[0].timing.tool_wait_ms, 42);
  });
});
