// Pure helpers behind the XIR structure map (质量 → XIR 结构): the static
// structure document from GET /api/xir/structure is indexed, laid out as a
// horizontal node-link tree, and overlaid with the detection-plugin catalog.
//
// The layout is the classic tidy node-link form: every visible leaf owns one
// vertical slot, an internal node sits at the midpoint of its first and last
// child, and depth owns the horizontal column. Nothing here touches React or
// the DOM, so the geometry and the plugin overlay are unit-testable.

export const XIR_CARD_W = 252;
export const XIR_CARD_H = 72;
export const XIR_COL_W = XIR_CARD_W + 96;
export const XIR_ROW_H = XIR_CARD_H + 18;
export const XIR_PAD = 28;

// Plugin identity colours. The seven builtin families each own one hue of the
// console's status vocabulary; an unknown future plugin cycles the same list.
// Colour is never the only carrier: every badge also has the plugin name.
const PLUGIN_COLORS = {
  secretmask: "var(--accent)",
  "access.indicators": "var(--info)",
  "behavior.dangerous_shell": "var(--danger)",
  "credential.regex": "var(--warning)",
  "pii.observation": "var(--violet)",
  "safety.provider_refusal": "var(--success)",
  "tool.error": "var(--viz-blue)",
};
const FALLBACK_COLORS = [
  "var(--accent)",
  "var(--info)",
  "var(--violet)",
  "var(--warning)",
  "var(--success)",
  "var(--danger)",
  "var(--viz-blue)",
];

export function pluginColor(pluginID, ordinal = 0) {
  return PLUGIN_COLORS[pluginID] || FALLBACK_COLORS[ordinal % FALLBACK_COLORS.length];
}

export function localizedName(item, language) {
  if (!item) return "";
  if (language === "en") return item.name || item.id || "";
  return item.name_zh || item.name || item.id || "";
}

export function localizedDescription(item, language) {
  if (!item) return "";
  if (language === "en") return item.description || "";
  return item.description_zh || item.description || "";
}

/** Index the structure document: lookup maps plus depth and parent links. */
export function buildStructureIndex(structure) {
  const nodes = structure?.nodes || [];
  const byId = new Map(nodes.map((node) => [node.id, node]));
  const parentOf = new Map();
  for (const node of nodes) {
    for (const child of node.children || []) parentOf.set(child, node.id);
  }
  const depthOf = new Map();
  const root = structure?.root || "";
  if (root && byId.has(root)) {
    depthOf.set(root, 0);
    const queue = [root];
    while (queue.length) {
      const id = queue.shift();
      for (const child of byId.get(id)?.children || []) {
        if (!depthOf.has(child)) {
          depthOf.set(child, depthOf.get(id) + 1);
          queue.push(child);
        }
      }
    }
  }
  return { nodes, byId, parentOf, depthOf, root, points: structure?.points || [], representations: structure?.representations || [] };
}

/** The depth-1 ancestor of a node: request, response, stream — or exchange. */
export function sectionOf(index, id) {
  let cur = id;
  while (index.parentOf.has(cur) && (index.depthOf.get(cur) ?? 0) > 1) {
    cur = index.parentOf.get(cur);
  }
  return (index.depthOf.get(cur) ?? 0) === 1 ? cur : "exchange";
}

/** The initial view: the first three levels open, deeper branches collapsed. */
export function defaultCollapsedIds(index) {
  const collapsed = new Set();
  for (const node of index.nodes) {
    if ((node.children || []).length && (index.depthOf.get(node.id) ?? 0) >= 2) {
      collapsed.add(node.id);
    }
  }
  return collapsed;
}

/**
 * Expand the ancestors of `ids` so those nodes become visible under the
 * default collapsed tree. A focused plugin's points and the hooks-only filter
 * both live below collapsed groups; without this the filter highlights nodes
 * the reader cannot see. Returns the same Set when nothing changed, so a React
 * state update can bail out.
 */
export function expandAncestors(index, ids, collapsed) {
  let next = collapsed;
  for (const id of ids || []) {
    let cur = index.parentOf.get(id);
    while (cur) {
      if (next.has(cur)) {
        if (next === collapsed) next = new Set(collapsed);
        next.delete(cur);
      }
      cur = index.parentOf.get(cur);
    }
  }
  return next;
}

/**
 * Lay out the visible tree. `collapsed` hides a node's children (the node
 * itself stays, with a count badge); `hidden` removes nodes entirely (the
 * hooks-only filter). Returns cards with world coordinates, parent->child
 * links, and the world bounds.
 */
export function layoutXirTree(index, { collapsed = new Set(), hidden = new Set() } = {}) {
  const cards = [];
  const links = [];
  let leafSlot = 0;
  let maxDepth = 0;

  const visit = (id, depth) => {
    const node = index.byId.get(id);
    if (!node || hidden.has(id)) return null;
    maxDepth = Math.max(maxDepth, depth);
    const kids = (node.children || []).filter((child) => !hidden.has(child));
    const isCollapsed = collapsed.has(id) && kids.length > 0;
    const childCards = [];
    if (!isCollapsed) {
      for (const child of kids) {
        const card = visit(child, depth + 1);
        if (card) childCards.push(card);
      }
    }
    let y;
    if (childCards.length) {
      y = (childCards[0].y + childCards[childCards.length - 1].y) / 2;
    } else {
      y = XIR_PAD + leafSlot * XIR_ROW_H + XIR_CARD_H / 2;
      leafSlot += 1;
    }
    const card = {
      id,
      node,
      depth,
      x: XIR_PAD + depth * XIR_COL_W,
      y,
      childCount: kids.length,
      collapsed: isCollapsed,
    };
    cards.push(card);
    for (const childCard of childCards) links.push({ from: card, to: childCard });
    return card;
  };

  if (index.root) visit(index.root, 0);
  // Tab order follows the visual order: top to bottom, then left to right.
  cards.sort((a, b) => a.y - b.y || a.x - b.x);
  const width = XIR_PAD * 2 + (maxDepth + 1) * XIR_COL_W - (XIR_COL_W - XIR_CARD_W);
  const height = XIR_PAD * 2 + Math.max(leafSlot, 1) * XIR_ROW_H - (XIR_ROW_H - XIR_CARD_H);
  return { cards, links, width, height };
}

/**
 * Index the plugin catalog by hook point. A plugin with no declared points is
 * not point-scoped and matches every point (detector.Plugin.HasPoint); it is
 * kept as a wildcard so the overlay mirrors the server's semantics.
 */
export function buildPointPluginIndex(plugins) {
  const map = new Map();
  const wildcards = [];
  for (const plugin of plugins || []) {
    const points = plugin.points || [];
    if (!points.length) {
      wildcards.push(plugin);
      continue;
    }
    for (const point of points) {
      if (!map.has(point)) map.set(point, []);
      map.get(point).push(plugin);
    }
  }
  return { map, wildcards };
}

/** The plugins acting on one node: its point's declarers plus the wildcards. */
export function pluginsForNode(node, pluginIndex) {
  if (!node?.point) return [];
  return [...(pluginIndex.map.get(node.point) || []), ...pluginIndex.wildcards];
}

/** Node IDs at least one (enabled) plugin hooks. Sealed nodes never qualify. */
export function hookedNodeIds(nodes, pluginIndex, { enabledOnly = true } = {}) {
  const hooked = new Set();
  for (const node of nodes || []) {
    if (!node.point) continue;
    const acting = pluginsForNode(node, pluginIndex).filter((plugin) => !enabledOnly || plugin.enabled);
    if (acting.length) hooked.add(node.id);
  }
  return hooked;
}

/**
 * The hooks-only filter keeps hooked nodes plus their ancestors (a branch is
 * the path to its hooks) and hides everything else.
 */
export function hooksOnlyHidden(nodes, index, hooked) {
  const keep = new Set();
  for (const id of hooked) {
    let cur = id;
    while (cur && !keep.has(cur)) {
      keep.add(cur);
      cur = index.parentOf.get(cur);
    }
  }
  const hidden = new Set();
  for (const node of nodes || []) {
    if (!keep.has(node.id)) hidden.add(node.id);
  }
  return hidden;
}

/** Coverage facts for the reading strip. */
export function structureStats(index, pluginIndex) {
  const pointNodes = index.nodes.filter((node) => node.point);
  const hooked = hookedNodeIds(index.nodes, pluginIndex, { enabledOnly: true });
  const hookedPointNodes = pointNodes.filter((node) => hooked.has(node.id));
  return {
    nodeCount: index.nodes.length,
    pointCount: index.points.length,
    pointNodeCount: pointNodes.length,
    hookedNodeCount: hookedPointNodes.length,
  };
}