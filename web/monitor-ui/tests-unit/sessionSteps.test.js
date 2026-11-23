import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
  barWidthPercent,
  buildLedgerRows,
  filterLedgerRows,
  formatRatioPercent,
  formatThroughput,
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

describe("formatting", () => {
  it("prints an em dash for a value that was not measured", () => {
    assert.equal(formatRatioPercent(null), "—");
    assert.equal(formatRatioPercent(undefined), "—");
    assert.equal(formatRatioPercent(0.5), "50.0%");
    assert.equal(formatThroughput(null), "—");
    assert.equal(formatThroughput(12.34), "12.3 tok/s");
  });
});
