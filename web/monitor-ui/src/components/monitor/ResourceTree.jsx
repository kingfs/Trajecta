import React, { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import {
  ChevronDown,
  ChevronRight,
  Crosshair,
  FileText,
  Folder,
  FolderOpen,
  Globe,
  Server,
} from "lucide-react";
import { Button } from "../ui/button";
import { InlineTag } from "../common/Badges";
import { formatDateTime } from "../../lib/monitor";
import {
  allDirectoryIDs,
  buildResourceForest,
  defaultExpandedIDs,
} from "../../lib/resourceTree";

/*
 * The resource index as directory trees.
 *
 * A flat list makes a reader reconstruct the common prefix of every row by eye:
 * a hundred reads under /data/src/git.example.net/ai/adp are a hundred rows that
 * all start the same way. Here that prefix is one collapsed row whose count is
 * the whole subtree, and opening it is the only way to spend vertical space.
 *
 * The counts on a row are the accesses its subtree accounts for - the number the
 * reader wanted before opening it - and the same value is repeated on each leaf
 * so a single file still reports how often it was touched.
 *
 * The paths and URLs are separate trees because their roots mean different
 * things: a path tree starts at / or ~ or a drive, while a URL tree starts at the
 * authority category (domain, ip, host) and then the authority itself.
 *
 * A row that names a resource also carries its locate action: hovering (or
 * focusing) the row reveals a crosshair that opens the trace conversation at the
 * step where the value was detected. The action lives beside the row rather than
 * inside it, because the leaf is already a link to the latest request and a
 * button cannot be nested in a link.
 */

const CATEGORY_LABELS = {
  url: "resources.categoryUrl",
  host: "resources.categoryHost",
  ip: "resources.categoryIp",
  domain: "resources.categoryDomain",
};

export function ResourceTree({ items = [], countKey = "access_count", t, leafHref, onLocate }) {
  const forest = useMemo(() => buildResourceForest(items, { countKey }), [items, countKey]);
  const initial = useMemo(() => {
    const ids = new Set();
    for (const id of defaultExpandedIDs(forest.paths)) {
      ids.add(id);
    }
    for (const id of defaultExpandedIDs(forest.urls)) {
      ids.add(id);
    }
    return ids;
  }, [forest]);
  // The default set depends on the data, and the reader's own toggles have to
  // survive a reparse or a filter change, so an explicit choice is kept aside
  // instead of being folded into the same set.
  const [overrides, setOverrides] = useState(() => new Map());
  // One locate request at a time: the server reparses the cassette to answer, so
  // a second click while the first is in flight is a queued duplicate, not a new
  // question.
  const [locatingKey, setLocatingKey] = useState("");

  const isOpen = (node) => (overrides.has(node.id) ? overrides.get(node.id) : initial.has(node.id));
  const toggle = (node) => {
    setOverrides((previous) => {
      const next = new Map(previous);
      next.set(node.id, !(previous.has(node.id) ? previous.get(node.id) : initial.has(node.id)));
      return next;
    });
  };
  const setAll = (open) => {
    const next = new Map();
    for (const id of allDirectoryIDs(forest.paths)) {
      next.set(id, open);
    }
    for (const id of allDirectoryIDs(forest.urls)) {
      next.set(id, open);
    }
    setOverrides(next);
  };

  const locate = async (item) => {
    if (!onLocate || locatingKey) {
      return;
    }
    setLocatingKey(`${item.kind}\u0000${item.value}`);
    try {
      await onLocate(item);
    } finally {
      setLocatingKey("");
    }
  };

  const metric = countKey === "trace_count" ? t("resources.traces") : t("resources.nodeCount");
  const sections = [
    { key: "paths", title: t("resources.treePaths"), roots: forest.paths.roots, total: forest.pathTotal },
    { key: "urls", title: t("resources.treeUrls"), roots: forest.urls.roots, total: forest.urlTotal },
  ].filter((section) => section.roots.length > 0);

  if (!sections.length) {
    return null;
  }

  return (
    <div className="resource-tree">
      <div className="resource-tree-toolbar">
        <p className="resource-tree-legend">{t("resources.treeLegend", { metric })}</p>
        <div className="resource-tree-toolbar-actions">
          <Button variant="ghost" type="button" onClick={() => setAll(true)}>{t("resources.expandAll")}</Button>
          <Button variant="ghost" type="button" onClick={() => setAll(false)}>{t("resources.collapseAll")}</Button>
        </div>
      </div>
      {sections.map((section) => (
        <section className="resource-tree-section" key={section.key}>
          <div className="resource-tree-section-head">
            <h3>{section.title}</h3>
            <InlineTag tone={section.key === "urls" ? "accent" : "green"}>
              {t("resources.count", { count: section.total })}
            </InlineTag>
          </div>
          <ul className="resource-tree-list">
            {section.roots.map((root) => (
              <ResourceNode
                key={root.id}
                node={root}
                depth={0}
                isOpen={isOpen}
                onToggle={toggle}
                t={t}
                leafHref={leafHref}
                metric={metric}
                onLocate={onLocate ? locate : null}
                locatingKey={locatingKey}
              />
            ))}
          </ul>
        </section>
      ))}
    </div>
  );
}

function ResourceNode({ node, depth, isOpen, onToggle, t, leafHref, metric, onLocate, locatingKey }) {
  const hasChildren = node.children.length > 0;
  const open = hasChildren && isOpen(node);
  const item = node.ownItems[0] ?? null;
  const href = item && leafHref ? leafHref(item) : "";
  const label = node.kind === "category" ? t(CATEGORY_LABELS[node.name] || "resources.categoryUrl") : node.name;
  const title = describeNode(node, t);
  const canLocate = Boolean(item && onLocate);
  const locateKey = item ? `${item.kind}\u0000${item.value}` : "";
  const locating = canLocate && locatingKey === locateKey;
  const locateLabel = item
    ? t("resources.locateHint", { value: item.value.length > 120 ? `${item.value.slice(0, 120)}…` : item.value })
    : "";
  const row = (
    <>
      {hasChildren ? (
        open ? <ChevronDown size={14} aria-hidden="true" /> : <ChevronRight size={14} aria-hidden="true" />
      ) : (
        <span className="resource-tree-spacer" aria-hidden="true" />
      )}
      {nodeIcon(node, open)}
      <span className={`resource-tree-name ${node.kind === "category" ? "" : "mono"}`.trim()}>{label}</span>
      {item && !hasChildren && href ? <span className="resource-tree-open">{t("resources.openLatestTrace")}</span> : null}
      <span className="resource-tree-count" title={t("resources.treeCountHint", { metric })}>{node.count}</span>
    </>
  );

  const rowButton = hasChildren ? (
    <button type="button" className="resource-tree-row-button" aria-expanded={open} onClick={() => onToggle(node)} title={title}>
      {row}
    </button>
  ) : href ? (
    <Link className="resource-tree-row-button" to={href} title={title}>
      {row}
    </Link>
  ) : (
    <span className="resource-tree-row-button" title={title}>{row}</span>
  );

  return (
    <li className={`resource-tree-node resource-tree-${node.kind}`}>
      <div className="resource-tree-row">
        {rowButton}
        {canLocate ? (
          <button
            type="button"
            className={`resource-tree-locate${locating ? " resource-tree-locate-busy" : ""}`.trim()}
            title={locateLabel}
            aria-label={locateLabel}
            aria-busy={locating ? "true" : "false"}
            disabled={locating}
            onClick={() => onLocate(item)}
          >
            <Crosshair size={13} aria-hidden="true" />
          </button>
        ) : null}
      </div>
      {open ? (
        <ul className="resource-tree-list">
          {node.children.map((child) => (
            <ResourceNode
              key={child.id}
              node={child}
              depth={depth + 1}
              isOpen={isOpen}
              onToggle={onToggle}
              t={t}
              leafHref={leafHref}
              metric={metric}
              onLocate={onLocate}
              locatingKey={locatingKey}
            />
          ))}
        </ul>
      ) : null}
    </li>
  );
}

function nodeIcon(node, open) {
  if (node.kind === "category" || node.kind === "host") {
    return <Globe size={13} aria-hidden="true" />;
  }
  if (node.kind === "dir" || node.kind === "root") {
    return open ? <FolderOpen size={13} aria-hidden="true" /> : <Folder size={13} aria-hidden="true" />;
  }
  if (node.kind === "endpoint") {
    return <Server size={13} aria-hidden="true" />;
  }
  return <FileText size={13} aria-hidden="true" />;
}

/* The tooltip carries what the row cannot: the exact value behind a directory
   row, and the counts and last-seen time of a leaf. A tree hides the full path
   of an inner node, so the title is where it stays available. */
function describeNode(node, t) {
  const item = node.ownItems[0] ?? null;
  if (!item) {
    return t("resources.treeSubtree", { count: node.count });
  }
  const parts = [item.value];
  if (item.trace_count !== undefined) {
    parts.push(`${t("resources.traces")}: ${item.trace_count}`);
  }
  if (item.access_count !== undefined) {
    parts.push(`${t("resources.nodeCount")}: ${item.access_count}`);
  }
  if (item.last_seen_at) {
    parts.push(`${t("resources.lastSeen")}: ${formatDateTime(item.last_seen_at)}`);
  }
  return parts.join("\n");
}
