/*
 * The resource index answers "which directories did this agent read, and which
 * hosts did it call". A flat list answers it one row at a time: a hundred reads
 * under one repository root are a hundred rows, and the common prefix - the fact
 * that everything happened under /data/src/git.example.net/ai/adp - is the part
 * the reader has to reconstruct by eye.
 *
 * This module turns the rows into the two forests that make the answer visible:
 *
 *   - local paths become one directory tree per root. The roots are the roots a
 *     path actually has (/ , ~ , C: , a UNC host, or "." for a relative path),
 *     because a warehouse of unrelated absolute paths under a single "/" row
 *     would hide the repository the agent worked in.
 *   - URLs become one tree per authority category (url, host, ip, domain), then
 *     the authority, then the URL path. The category is what separates a public
 *     API host from a LAN address from localhost.
 *
 * Counts aggregate up the tree: a directory row reports how many accesses its
 * whole subtree accounts for, so the number on the collapsed row is the number
 * the reader wanted before opening it.
 *
 * The functions are pure and exported for the unit tests; the component only
 * decides what is open and how a row is drawn.
 */

const PATH_SEPARATOR = "/";

export const URL_CATEGORIES = ["url", "host", "ip", "domain"];

/*
 * A resource row as the API returns it: kind, value, and the two counts. The
 * catalogue exposes trace_count (requests) and access_count (steps); a trace
 * exposes access_count only. The caller names the field its page counts.
 */
export function buildResourceForest(items = [], { countKey = "access_count" } = {}) {
  const paths = createForest();
  const urls = createForest();
  for (const item of Array.isArray(items) ? items : []) {
    if (!item || typeof item.value !== "string" || item.value === "") {
      continue;
    }
    if (item.kind === "url") {
      insertURL(urls, item, countKey);
    } else {
      insertPath(paths, item, countKey);
    }
  }
  finalizeForest(paths);
  finalizeForest(urls);
  return {
    paths,
    urls,
    pathTotal: paths.itemTotal,
    urlTotal: urls.itemTotal,
  };
}

function createForest() {
  return { roots: [], byID: new Map(), itemTotal: 0 };
}

/* A stable id for a node. The kind separates a path segment from a URL segment
   with the same text, and the root keeps a relative path from colliding with an
   absolute one that has the same tail. */
function nodeID(scope, parts) {
  return `${scope}\u0000${parts.join(PATH_SEPARATOR)}`;
}

function rootNode(forest, name, kind) {
  const id = nodeID(forest.scope, [name]);
  let root = forest.byID.get(id);
  if (!root) {
    root = createNode(id, name, kind);
    forest.byID.set(id, root);
    forest.roots.push(root);
  }
  return root;
}

function childNode(parent, name, kind, id) {
  let child = parent.byName.get(name);
  if (!child) {
    child = createNode(id, name, kind);
    parent.byName.set(name, child);
    parent.children.push(child);
  }
  return child;
}

function createNode(id, name, kind) {
  return {
    id,
    name,
    kind,
    count: 0,
    ownItems: [],
    children: [],
    byName: new Map(),
  };
}

/* --- local paths ------------------------------------------------------- */

/*
 * Splitting a path for display, not for the filesystem.
 *
 * A Windows path arrives with backslashes, which the index normalized to single
 * separators; a UNC path keeps its doubled root. Both are read with '/' after
 * the backslashes are replaced, so one tree holds every form the detector
 * accepts. The root is kept as its own segment because "/" is where a POSIX path
 * starts and "C:" is where a Windows path starts - neither is a directory name.
 */
export function splitResourcePath(value) {
  const normalized = String(value).replace(/\\/g, "/");
  if (normalized.startsWith("//")) {
    return { root: "//", segments: normalized.slice(2).split("/").filter(Boolean) };
  }
  if (normalized.startsWith("/")) {
    return { root: "/", segments: normalized.slice(1).split("/").filter(Boolean) };
  }
  if (normalized.startsWith("~/")) {
    return { root: "~", segments: normalized.slice(2).split("/").filter(Boolean) };
  }
  const drive = /^([A-Za-z]:)\//.exec(normalized);
  if (drive) {
    return { root: drive[1], segments: normalized.slice(3).split("/").filter(Boolean) };
  }
  return { root: ".", segments: normalized.split("/").filter(Boolean) };
}

function insertPath(forest, item, countKey) {
  forest.scope = "path";
  const { root, segments } = splitResourcePath(item.value);
  if (segments.length === 0) {
    return;
  }
  const rootEntry = rootNode(forest, root, "root");
  let parent = rootEntry;
  const seen = [root];
  for (let i = 0; i < segments.length; i++) {
    const name = segments[i];
    seen.push(name);
    const last = i === segments.length - 1;
    parent = childNode(parent, name, last ? "file" : "dir", nodeID("path", seen));
  }
  parent.ownItems.push(item);
  parent.count += itemCount(item, countKey);
  forest.itemTotal++;
}

/* --- URLs -------------------------------------------------------------- */

const IPV4 = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/;

/*
 * The four roots the URL side is grouped by:
 *
 *   - ip      an address literal (v4 or v6); a LAN endpoint is not a domain
 *   - domain  a name with a dot and a textual top-level label (api.github.com)
 *   - host    a single-label name (localhost, an internal box name)
 *   - url     an authority that is not a host at all: a URL the parser could
 *             not read, or one with no authority
 *
 * "domain" is checked after "ip" so 10.0.0.5 cannot read as a four-label domain.
 */
export function classifyURLHost(hostname) {
  const host = String(hostname || "");
  if (host === "") {
    return "url";
  }
  const bare = host.replace(/^\[/, "").replace(/\]$/, "");
  if (IPV4.test(bare) && bare.split(".").every((part) => Number(part) <= 255)) {
    return "ip";
  }
  if (bare.includes(":")) {
    return "ip";
  }
  if (!host.includes(".")) {
    return "host";
  }
  const labels = host.split(".");
  const top = labels[labels.length - 1];
  if (/^[A-Za-z]{2,}$/.test(top) && labels.every((label) => /^[A-Za-z0-9-]+$/.test(label))) {
    return "domain";
  }
  return "url";
}

/* The authority is what a reader recognises: the host and, when it is not the
   scheme default, the port. Two schemes on one host share the authority row;
   the scheme stays in the leaf's title. */
export function urlAuthority(parsed) {
  const host = parsed.hostname || "";
  return parsed.port ? `${host}:${parsed.port}` : host;
}

function urlPathSegments(pathname) {
  return String(pathname || "").split("/").filter(Boolean);
}

function urlQuerySuffix(parsed) {
  const search = parsed.search || "";
  const hash = parsed.hash || "";
  if (!search && !hash) {
    return "";
  }
  const query = search.length > 32 ? `${search.slice(0, 32)}…` : search;
  return `${query}${hash.slice(0, 16)}`;
}

function urlLeafName(parsed) {
  const segments = urlPathSegments(parsed.pathname);
  const base = segments.length ? segments[segments.length - 1] : "/";
  const suffix = urlQuerySuffix(parsed);
  return suffix ? `${base} ${suffix}` : base;
}

function insertURL(forest, item, countKey) {
  forest.scope = "url";
  let parsed = null;
  try {
    parsed = new URL(item.value);
  } catch {
    parsed = null;
  }
  if (!parsed) {
    // A value the detector accepted but the URL parser cannot read has no host
    // to group by; it stays a leaf under the fallback root rather than being
    // dropped, because the audit saw it.
    const fallback = rootNode(forest, "url", "category");
    const leaf = childNode(fallback, item.value, "endpoint", nodeID("url", ["url", item.value]));
    leaf.ownItems.push(item);
    leaf.count += itemCount(item, countKey);
    forest.itemTotal++;
    return;
  }
  const category = classifyURLHost(parsed.hostname);
  const categoryNode = rootNode(forest, category, "category");
  const authority = urlAuthority(parsed);
  const authorityNode = childNode(
    categoryNode,
    authority,
    "host",
    nodeID("url", [category, authority]),
  );
  const segments = urlPathSegments(parsed.pathname);
  let parent = authorityNode;
  const seen = [category, authority];
  for (let i = 0; i < segments.length; i++) {
    const last = i === segments.length - 1;
    seen.push(segments[i]);
    // Two URLs may differ only in the query; the leaf id carries the query so
    // the two rows do not share a React key under one directory.
    const id =
      last && urlQuerySuffix(parsed)
        ? nodeID("url", [...seen, urlQuerySuffix(parsed)])
        : nodeID("url", seen);
    parent = childNode(parent, last ? urlLeafName(parsed) : segments[i], last ? "endpoint" : "dir", id);
  }
  parent.ownItems.push(item);
  parent.count += itemCount(item, countKey);
  forest.itemTotal++;
}

/* --- shared ------------------------------------------------------------ */

function itemCount(item, countKey) {
  const value = Number(item?.[countKey] ?? item?.access_count ?? 0);
  return Number.isFinite(value) && value > 0 ? value : 0;
}

/* Aggregate counts up the tree, drop the scratch map, and order every level the
   way the page reads: the busiest subtree first, then directories before files,
   then by name. */
function finalizeForest(forest) {
  const visit = (node) => {
    for (const child of node.children) {
      visit(child);
    }
    for (const child of node.children) {
      node.count += child.count;
    }
    node.children.sort(compareNodes);
    delete node.byName;
  };
  for (const root of forest.roots) {
    visit(root);
  }
  forest.roots.sort(compareNodes);
  // The URL roots keep the fixed order the page documents (url, host, ip,
  // domain) instead of sorting by traffic: a category is a fixed place in the
  // panel, so it must not move when a different host becomes the busiest one.
  if (forest.scope === "url") {
    forest.roots.sort((left, right) => URL_CATEGORIES.indexOf(left.name) - URL_CATEGORIES.indexOf(right.name));
  }
  forest.byID.clear();
  delete forest.byID;
  delete forest.scope;
}

function compareNodes(a, b) {
  if (b.count !== a.count) {
    return b.count - a.count;
  }
  const rank = { root: 0, category: 1, host: 2, dir: 3, file: 4, endpoint: 5 };
  const left = rank[a.kind] ?? 6;
  const right = rank[b.kind] ?? 6;
  if (left !== right) {
    return left - right;
  }
  return a.name.localeCompare(b.name);
}

/* How many leaf rows the forest has - what "too many to open at once" is
   measured against. */
export function countForestLeaves(forest) {
  let leaves = 0;
  const visit = (node) => {
    if (node.children.length === 0) {
      leaves++;
      return;
    }
    for (const child of node.children) {
      visit(child);
    }
  };
  for (const root of forest?.roots ?? []) {
    visit(root);
  }
  return leaves;
}

/* The directory rows that isOpen should contain on first paint: everything when
   the tree is small enough to read, otherwise the roots and their first level,
   which is the repository and its top directories. */
export function defaultExpandedIDs(forest, { maxLeaves = 60, minDepth = 2 } = {}) {
  const ids = new Set();
  if (!forest?.roots) {
    return ids;
  }
  const expandAll = countForestLeaves(forest) <= maxLeaves;
  const visit = (node, depth) => {
    if (node.children.length === 0) {
      return;
    }
    if (expandAll || depth < minDepth) {
      ids.add(node.id);
    }
    for (const child of node.children) {
      visit(child, depth + 1);
    }
  };
  for (const root of forest.roots) {
    visit(root, 0);
  }
  return ids;
}

/* Every directory row, for an explicit "expand all". */
export function allDirectoryIDs(forest) {
  const ids = new Set();
  const visit = (node) => {
    if (node.children.length === 0) {
      return;
    }
    ids.add(node.id);
    for (const child of node.children) {
      visit(child);
    }
  };
  for (const root of forest?.roots ?? []) {
    visit(root);
  }
  return ids;
}
