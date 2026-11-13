import test from "node:test";
import assert from "node:assert/strict";
import {
  XIR_CARD_H, XIR_CARD_W, XIR_COL_W, XIR_PAD,
  buildPointPluginIndex, buildStructureIndex, defaultCollapsedIds,
  expandAncestors, hookedNodeIds, hooksOnlyHidden, layoutXirTree, pluginColor,
  pluginsForNode, sectionOf, structureStats,
} from "../src/lib/xirStructure.js";

// A miniature structure with the same shape as the server document: a root,
// two sections, a group, a hooked part and a sealed part.
const STRUCTURE = {
  version: 1,
  root: "exchange",
  representations: [],
  points: [
    { point: "request.message" },
    { point: "response.output" },
    { point: "exchange.finalize" },
  ],
  nodes: [
    { id: "exchange", kind: "root", type: "xir.Exchange", name: "Exchange", name_zh: "交换", point: "exchange.finalize", children: ["request", "response"] },
    { id: "request", kind: "section", type: "xir.Request", name: "Request", name_zh: "请求", children: ["request.messages"] },
    { id: "request.messages", kind: "group", type: "[]xir.Message", name: "Messages", name_zh: "会话消息", children: ["request.text", "request.sealed"] },
    { id: "request.text", kind: "part", type: "xir.Part(text)", name: "Text", name_zh: "文本", point: "request.message" },
    { id: "request.sealed", kind: "sealed", type: "xir.Sealed", name: "Sealed", name_zh: "密封块", flags: ["sealed", "no-text"] },
    { id: "response", kind: "section", type: "xir.Response", name: "Response", name_zh: "响应", children: ["response.text"] },
    { id: "response.text", kind: "part", type: "xir.Part(text)", name: "Text", name_zh: "文本", point: "response.output" },
  ],
};

const PLUGINS = [
  { id: "secretmask", name: "Masking", enabled: true, points: ["request.message", "response.output"] },
  { id: "access.indicators", name: "Indicators", enabled: false, points: ["request.message"] },
  { id: "wildcard", name: "Wildcard", enabled: true, points: [] },
];

function index() {
  return buildStructureIndex(STRUCTURE);
}

test("buildStructureIndex derives parents and depths", () => {
  const idx = index();
  assert.equal(idx.byId.size, 7);
  assert.equal(idx.parentOf.get("request.text"), "request.messages");
  assert.equal(idx.depthOf.get("exchange"), 0);
  assert.equal(idx.depthOf.get("request"), 1);
  assert.equal(idx.depthOf.get("request.text"), 3);
  assert.equal(sectionOf(idx, "request.text"), "request");
  assert.equal(sectionOf(idx, "response.text"), "response");
  assert.equal(sectionOf(idx, "exchange"), "exchange");
});

test("layoutXirTree places leaves in slots and parents at child midpoints", () => {
  const idx = index();
  const layout = layoutXirTree(idx, { collapsed: new Set(), hidden: new Set() });
  assert.equal(layout.cards.length, 7);
  assert.equal(layout.links.length, 6);
  const byId = new Map(layout.cards.map((card) => [card.id, card]));
  // Columns follow depth.
  assert.equal(byId.get("exchange").x, XIR_PAD);
  assert.equal(byId.get("request").x, XIR_PAD + XIR_COL_W);
  assert.equal(byId.get("request.text").x, XIR_PAD + 3 * XIR_COL_W);
  // The root sits at the midpoint of its two section children.
  const request = byId.get("request");
  const response = byId.get("response");
  assert.equal(byId.get("exchange").y, (request.y + response.y) / 2);
  // Bounds cover every card.
  for (const card of layout.cards) {
    assert.ok(card.x >= 0 && card.x + XIR_CARD_W <= layout.width);
    assert.ok(card.y - XIR_CARD_H / 2 >= 0 && card.y + XIR_CARD_H / 2 <= layout.height);
  }
});

test("collapsed nodes keep their card and hide their children", () => {
  const idx = index();
  const layout = layoutXirTree(idx, { collapsed: new Set(["request.messages"]), hidden: new Set() });
  const byId = new Map(layout.cards.map((card) => [card.id, card]));
  assert.ok(byId.has("request.messages"));
  assert.equal(byId.get("request.messages").collapsed, true);
  assert.equal(byId.get("request.messages").childCount, 2);
  assert.ok(!byId.has("request.text"));
  assert.ok(!byId.has("request.sealed"));
});

test("defaultCollapsedIds opens the first three levels", () => {
  const idx = index();
  const collapsed = defaultCollapsedIds(idx);
  // request.messages is depth 2 with children: collapsed by default.
  assert.ok(collapsed.has("request.messages"));
  assert.ok(!collapsed.has("request"));
  assert.ok(!collapsed.has("exchange"));
});

test("expandAncestors opens the path to a focused node", () => {
  const idx = index();
  const collapsed = defaultCollapsedIds(idx);
  assert.ok(collapsed.has("request.messages"));

  const expanded = expandAncestors(idx, ["request.text"], collapsed);
  assert.ok(!expanded.has("request.messages"), "the collapsed ancestor must open");
  assert.notStrictEqual(expanded, collapsed);

  // The original set is not mutated, and no-op calls keep identity.
  assert.ok(collapsed.has("request.messages"));
  const untouched = expandAncestors(idx, ["request"], expanded);
  assert.strictEqual(untouched, expanded);
});

test("hidden nodes disappear with their subtree", () => {
  const idx = index();
  const layout = layoutXirTree(idx, { collapsed: new Set(), hidden: new Set(["response"]) });
  const ids = layout.cards.map((card) => card.id);
  assert.ok(!ids.includes("response"));
  assert.ok(!ids.includes("response.text"));
  assert.ok(ids.includes("request"));
});

test("plugin overlay mirrors HasPoint semantics including wildcards", () => {
  const pluginIndex = buildPointPluginIndex(PLUGINS);
  const idx = index();
  const text = idx.byId.get("request.text");
  const acting = pluginsForNode(text, pluginIndex);
  assert.deepEqual(acting.map((plugin) => plugin.id), ["secretmask", "access.indicators", "wildcard"]);
  // A sealed node carries no point, so nothing acts on it.
  assert.deepEqual(pluginsForNode(idx.byId.get("request.sealed"), pluginIndex), []);
  // The root point is only covered by the wildcard.
  assert.deepEqual(pluginsForNode(idx.byId.get("exchange"), pluginIndex).map((p) => p.id), ["wildcard"]);
});

test("hookedNodeIds counts enabled plugins only", () => {
  const pluginIndex = buildPointPluginIndex(PLUGINS);
  const idx = index();
  const hooked = hookedNodeIds(idx.nodes, pluginIndex, { enabledOnly: true });
  assert.ok(hooked.has("request.text"));
  assert.ok(hooked.has("response.text"));
  assert.ok(hooked.has("exchange")); // wildcard is enabled
  assert.ok(!hooked.has("request.sealed"));
  const all = hookedNodeIds(idx.nodes, pluginIndex, { enabledOnly: false });
  assert.equal(all.size, hooked.size); // indicators adds no new node here
});

test("hooksOnlyHidden keeps hooked nodes and their ancestors", () => {
  const idx = index();
  const pluginIndex = buildPointPluginIndex([PLUGINS[0]]); // secretmask only
  const hooked = hookedNodeIds(idx.nodes, pluginIndex, { enabledOnly: true });
  const hidden = hooksOnlyHidden(idx.nodes, idx, hooked);
  assert.ok(!hidden.has("request.text"));
  assert.ok(!hidden.has("request.messages"));
  assert.ok(!hidden.has("request"));
  assert.ok(!hidden.has("exchange"));
  assert.ok(hidden.has("request.sealed"));
  // response.text is hooked by secretmask too, so the response branch stays.
  assert.ok(!hidden.has("response"));
  assert.ok(!hidden.has("response.text"));
});

test("structureStats reports coverage of point nodes", () => {
  const idx = index();
  const pluginIndex = buildPointPluginIndex([PLUGINS[0]]);
  const stats = structureStats(idx, pluginIndex);
  assert.equal(stats.nodeCount, 7);
  assert.equal(stats.pointCount, 3);
  assert.equal(stats.pointNodeCount, 3); // exchange, request.text, response.text
  // secretmask declares the two text points but not exchange.finalize, so the
  // root point node is not covered by this plugin alone.
  assert.equal(stats.hookedNodeCount, 2);
});

test("pluginColor is stable for known ids and cycles for unknown ones", () => {
  assert.equal(pluginColor("secretmask"), "var(--accent)");
  assert.equal(pluginColor("tool.error"), "var(--viz-blue)");
  assert.ok(pluginColor("future.plugin", 0));
  assert.equal(pluginColor("future.plugin", 7), pluginColor("future.plugin", 0));
});