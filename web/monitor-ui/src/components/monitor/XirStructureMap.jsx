import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Activity, AlertTriangle, Brain, ClipboardList, Cpu, Expand, FileText, Filter,
  Globe, Hash, Image as ImageIcon, Info, Link2, ListTree, Lock, Mail, Maximize2,
  MessageSquare, Network, Quote, Radio, Send, ShieldAlert, Shrink,
  SlidersHorizontal, Tag, Waves, Wrench, X, ZoomIn, ZoomOut,
} from "lucide-react";
import { Button } from "../ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "../ui/popover";
import { useI18n } from "../../lib/i18n";
import {
  XIR_CARD_H, XIR_CARD_W, XIR_PAD,
  defaultCollapsedIds, hookedNodeIds, hooksOnlyHidden, layoutXirTree,
  localizedName, pluginColor, pluginsForNode, sectionOf,
} from "../../lib/xirStructure";

/*
 * XirStructureMap is the graphical half of 质量 → XIR 结构: the static logical
 * structure of the Exchange IR drawn as a horizontal node-link tidy tree, with
 * the detection-plugin catalog overlaid on the hook points.
 *
 * The form is the classic node-link diagram rather than a space-filling
 * treemap/icicle: the IR has no meaningful size dimension, and the reader's
 * question is structural ("what hangs off what, and which plugin observes this
 * node"), which node-link answers directly and leaves room for per-node
 * badges. Edges are cubic Béziers; a node an enabled plugin hooks carries the
 * plugin's colour and an animated flow along its incoming edge.
 *
 * Rendering is a hybrid: one transformed "world" holds an SVG edge layer and
 * HTML node cards, so text, tooltips and keyboard focus stay native while
 * pan/zoom is a single CSS transform. No chart library is needed for a static
 * tree of ~40 nodes.
 */

const SECTION_CLASS = {
  exchange: "xir-sec-exchange",
  request: "xir-sec-request",
  response: "xir-sec-response",
  stream: "xir-sec-stream",
};

// One icon per structural position; the fallback follows the node kind.
function nodeIcon(node) {
  const id = node.id;
  if (node.flags?.includes("sealed")) return Lock;
  if (node.flags?.includes("media")) return ImageIcon;
  if (id.endsWith(".tool_call") || id === "response.tool_call") return Wrench;
  if (id.includes("tool_result")) return ClipboardList;
  if (id.includes("reasoning")) return Brain;
  if (id.endsWith(".refusal")) return ShieldAlert;
  if (id.endsWith(".citation")) return Quote;
  if (id.endsWith(".search")) return Globe;
  if (id === "exchange") return Network;
  if (id === "request") return Send;
  if (id === "response") return MessageSquare;
  if (id === "stream") return Waves;
  if (id === "request.envelope") return Mail;
  if (id === "request.model") return Cpu;
  if (id === "request.stream") return Radio;
  if (id === "request.params") return SlidersHorizontal;
  if (id === "request.metadata") return Tag;
  if (id === "request.state") return Link2;
  if (id === "response.usage") return Hash;
  if (id === "response.stop") return Activity;
  if (id === "response.error") return AlertTriangle;
  if (id === "stream.lifecycle") return Activity;
  if (node.kind === "group") return ListTree;
  if (node.kind === "field") return Hash;
  return FileText;
}

const clamp = (value, lo, hi) => Math.min(hi, Math.max(lo, value));

function edgePath(from, to) {
  const fx = from.x + XIR_CARD_W;
  const fy = from.y;
  const tx = to.x;
  const ty = to.y;
  const dx = Math.max((tx - fx) * 0.5, 42);
  return `M ${fx} ${fy} C ${fx + dx} ${fy}, ${tx - dx} ${ty}, ${tx} ${ty}`;
}

export function XirStructureMap({
  index,
  pluginIndex,
  focusPlugin = null,
  selectedId = "",
  onSelect,
}) {
  const { t, language } = useI18n();
  const containerRef = useRef(null);
  const [size, setSize] = useState({ w: 960, h: 600 });
  const [view, setView] = useState({ x: 40, y: 40, k: 0.8 });
  const [collapsed, setCollapsed] = useState(() => new Set());
  const [hooksOnly, setHooksOnly] = useState(false);
  const [panning, setPanning] = useState(false);
  const [fullscreen, setFullscreen] = useState(false);
  const [fsFallback, setFsFallback] = useState(false);
  const fitRequested = useRef(true);
  const pointers = useRef(new Map());
  const pinch = useRef(null);

  // The initial branch state opens the first three levels; a new structure
  // document (there is only one, static) resets it.
  useEffect(() => {
    setCollapsed(defaultCollapsedIds(index));
    fitRequested.current = true;
  }, [index]);

  const hooked = useMemo(
    () => hookedNodeIds(index.nodes, pluginIndex, { enabledOnly: true }),
    [index, pluginIndex],
  );
  const hooksFilterActive = hooksOnly && hooked.size > 0;
  const hidden = useMemo(
    () => (hooksFilterActive ? hooksOnlyHidden(index.nodes, index, hooked) : new Set()),
    [hooksFilterActive, index, hooked],
  );
  const layout = useMemo(
    () => layoutXirTree(index, { collapsed, hidden }),
    [index, collapsed, hidden],
  );

  // Per-node overlay facts, computed once per layout.
  const cardFacts = useMemo(() => {
    const facts = new Map();
    for (const card of layout.cards) {
      const acting = pluginsForNode(card.node, pluginIndex);
      const enabledActing = acting.filter((plugin) => plugin.enabled);
      const focusMatch = Boolean(focusPlugin) && acting.some((plugin) => plugin.id === focusPlugin.id);
      facts.set(card.id, {
        section: sectionOf(index, card.id),
        acting,
        enabledActing,
        focusMatch,
        dimmed: Boolean(focusPlugin) && !focusMatch,
      });
    }
    return facts;
  }, [layout, pluginIndex, focusPlugin, index]);

  const fit = useCallback(() => {
    const pad = 56;
    const k = clamp(
      Math.min((size.w - pad) / layout.width, (size.h - pad) / layout.height, 1.1),
      0.2,
      2.5,
    );
    setView({
      k,
      x: (size.w - layout.width * k) / 2,
      y: (size.h - layout.height * k) / 2,
    });
  }, [size, layout.width, layout.height]);

  // The opening frame is readable, not a fit-to-overview: the tree is anchored
  // top-left at a scale where node labels stay legible, and the reader pans or
  // presses fit for the whole picture. Fitting a 40-node tree into one screen
  // shrinks the labels below reading size, which reads as a broken render.
  const anchorTop = useCallback(() => {
    if (!layout.cards.length) return;
    const k = clamp(size.h / 760, 0.65, 1);
    let minX = Infinity;
    let minY = Infinity;
    for (const card of layout.cards) {
      minX = Math.min(minX, card.x);
      minY = Math.min(minY, card.y - XIR_CARD_H / 2);
    }
    setView({ k, x: 48 - minX * k, y: 64 - minY * k });
  }, [size, layout]);

  useEffect(() => {
    const el = containerRef.current;
    if (!el) return undefined;
    const ro = new ResizeObserver((entries) => {
      const rect = entries[0].contentRect;
      setSize({ w: rect.width, h: rect.height });
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  useEffect(() => {
    if (fitRequested.current) {
      fitRequested.current = false;
      anchorTop();
    }
  }, [anchorTop, layout.width, layout.height]);

  const zoomAt = useCallback((px, py, factor) => {
    setView((v) => {
      const k = clamp(v.k * factor, 0.2, 2.5);
      const f = k / v.k;
      return { k, x: px - (px - v.x) * f, y: py - (py - v.y) * f };
    });
  }, []);

  // Wheel zoom needs a non-passive listener to keep the page from scrolling.
  useEffect(() => {
    const el = containerRef.current;
    if (!el) return undefined;
    const onWheel = (event) => {
      event.preventDefault();
      const rect = el.getBoundingClientRect();
      zoomAt(event.clientX - rect.left, event.clientY - rect.top, Math.exp(-event.deltaY * 0.0014));
    };
    el.addEventListener("wheel", onWheel, { passive: false });
    return () => el.removeEventListener("wheel", onWheel);
  }, [zoomAt]);

  const isInteractive = (target) =>
    Boolean(target.closest?.(".xir-card, .xir-toolbar, .xir-hint, button, a, input, [role='switch'], [role='dialog']"));

  const onPointerDown = (event) => {
    if (isInteractive(event.target)) return;
    const el = containerRef.current;
    el.setPointerCapture?.(event.pointerId);
    pointers.current.set(event.pointerId, { x: event.clientX, y: event.clientY });
    if (pointers.current.size === 2) {
      const [a, b] = [...pointers.current.values()];
      pinch.current = { dist: Math.hypot(a.x - b.x, a.y - b.y), k: view.k };
    }
    setPanning(true);
  };

  const onPointerMove = (event) => {
    if (!pointers.current.has(event.pointerId)) return;
    const prev = pointers.current.get(event.pointerId);
    pointers.current.set(event.pointerId, { x: event.clientX, y: event.clientY });
    if (pointers.current.size === 2 && pinch.current) {
      const [a, b] = [...pointers.current.values()];
      const dist = Math.hypot(a.x - b.x, a.y - b.y);
      const rect = containerRef.current.getBoundingClientRect();
      const mx = (a.x + b.x) / 2 - rect.left;
      const my = (a.y + b.y) / 2 - rect.top;
      const target = clamp(pinch.current.k * (dist / (pinch.current.dist || 1)), 0.2, 2.5);
      zoomAt(mx, my, target / view.k);
      return;
    }
    setView((v) => ({ ...v, x: v.x + event.clientX - prev.x, y: v.y + event.clientY - prev.y }));
  };

  const onPointerUp = (event) => {
    pointers.current.delete(event.pointerId);
    if (pointers.current.size < 2) pinch.current = null;
    if (pointers.current.size === 0) setPanning(false);
  };

  const toggleCollapse = (id) => {
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const expandAll = () => {
    setCollapsed(new Set());
    fitRequested.current = true;
  };
  const collapseDefault = () => {
    setCollapsed(defaultCollapsedIds(index));
    fitRequested.current = true;
  };
  const requestFit = () => {
    fitRequested.current = false;
    fit();
  };

  const zoomButton = (factor) => {
    zoomAt(size.w / 2, size.h / 2, factor);
  };

  // Fullscreen: the canvas — toolbar, hint and legend included — takes over
  // the whole screen through the native Fullscreen API. Where the API is
  // missing or refused (iOS Safari, some embedded webviews) a fixed-position
  // overlay is the fallback, exited with the same button or Escape.
  const isFull = fullscreen || fsFallback;
  const toggleFullscreen = useCallback(async () => {
    const el = containerRef.current;
    if (!el) return;
    if (document.fullscreenElement === el) {
      try {
        await document.exitFullscreen();
      } catch {
        // The fullscreenchange event reconciles the state either way.
      }
      return;
    }
    if (fsFallback) {
      setFsFallback(false);
      fitRequested.current = true;
      return;
    }
    if (typeof el.requestFullscreen === "function") {
      try {
        await el.requestFullscreen();
        return;
      } catch {
        // Fall through to the overlay.
      }
    }
    setFsFallback(true);
    fitRequested.current = true;
  }, [fsFallback]);

  useEffect(() => {
    const onChange = () => {
      setFullscreen(document.fullscreenElement === containerRef.current);
      // Re-anchor for the new viewport: fullscreen gains a lot of height, and
      // the ResizeObserver delivers it right after this event.
      fitRequested.current = true;
    };
    document.addEventListener("fullscreenchange", onChange);
    return () => document.removeEventListener("fullscreenchange", onChange);
  }, []);

  useEffect(() => {
    if (!fsFallback) return undefined;
    const onKey = (event) => {
      if (event.key === "Escape") {
        setFsFallback(false);
        fitRequested.current = true;
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [fsFallback]);

  const focusColor = focusPlugin ? pluginColor(focusPlugin.id) : "";

  return (
    <div
      ref={containerRef}
      className={`xir-canvas ${panning ? "is-panning" : ""} ${fsFallback ? "is-fullscreen-fallback" : ""}`}
      role="group"
      aria-label={t("xir.mapAria")}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
      onPointerCancel={onPointerUp}
      onDoubleClick={(event) => {
        if (!isInteractive(event.target)) requestFit();
      }}
    >
      <p className="xir-sr-only">{t("xir.mapDescription")}</p>
      <div
        className="xir-world"
        style={{
          width: layout.width,
          height: layout.height,
          transform: `translate(${view.x}px, ${view.y}px) scale(${view.k})`,
        }}
      >
        <svg className="xir-edges" width={layout.width} height={layout.height} aria-hidden="true">
          {layout.links.map((link) => {
            const facts = cardFacts.get(link.to.id);
            const section = facts?.section || "exchange";
            const flowPlugin = focusPlugin
              ? (facts?.focusMatch && focusPlugin.enabled ? focusPlugin : null)
              : facts?.enabledActing[0];
            const dimmed = Boolean(focusPlugin) && !facts?.focusMatch;
            const d = edgePath(link.from, link.to);
            return (
              <g
                key={`${link.from.id}->${link.to.id}`}
                className={`xir-edge ${SECTION_CLASS[section]} ${dimmed ? "is-dimmed" : ""}`}
              >
                <path className="xir-edge-base" d={d} />
                {flowPlugin ? (
                  <path
                    className="xir-edge-flow"
                    d={d}
                    style={{ stroke: pluginColor(flowPlugin.id) }}
                  />
                ) : null}
              </g>
            );
          })}
        </svg>

        {layout.cards.map((card, i) => {
          const facts = cardFacts.get(card.id);
          const node = card.node;
          const Icon = nodeIcon(node);
          const sealed = node.flags?.includes("sealed");
          const selected = selectedId === node.id;
          const dots = facts.enabledActing;
          const ringColor = facts.focusMatch ? focusColor : "";
          return (
            <div
              key={card.id}
              className={[
                "xir-card",
                SECTION_CLASS[facts.section],
                node.kind === "root" ? "xir-root" : "",
                sealed ? "is-sealed" : "",
                selected ? "is-selected" : "",
                facts.focusMatch ? "is-focus-match" : "",
                facts.dimmed ? "is-dimmed" : "",
                dots.length ? "is-hooked" : "",
              ].filter(Boolean).join(" ")}
              style={{
                left: card.x,
                top: card.y - XIR_CARD_H / 2,
                width: XIR_CARD_W,
                height: XIR_CARD_H,
                "--enter-delay": `${Math.min(i * 16, 360)}ms`,
                ...(ringColor ? { "--ring-color": ringColor } : {}),
              }}
              role="button"
              tabIndex={0}
              aria-pressed={selected}
              aria-label={`${localizedName(node, language)}, ${node.type}${node.point ? `, ${t("xir.pointLabel")} ${node.point}` : ""}${dots.length ? `, ${t("xir.pluginCount", { count: dots.length })}` : ""}`}
              onClick={() => onSelect?.(node.id)}
              onKeyDown={(event) => {
                if (event.key === "Enter" || event.key === " ") {
                  event.preventDefault();
                  onSelect?.(node.id);
                }
              }}
            >
              <span className="xir-card-spine" aria-hidden="true" />
              <div className="xir-card-main">
                <span className="xir-card-icon" aria-hidden="true">
                  <Icon size={14} strokeWidth={1.8} />
                </span>
                <span className="xir-card-titles">
                  <span className="xir-card-name">{localizedName(node, language)}</span>
                  <span className="xir-card-type" title={node.type}>{node.type}</span>
                </span>
                {sealed ? <Lock className="xir-card-lock" size={12} aria-hidden="true" /> : null}
              </div>
              <div className="xir-card-foot">
                {node.point ? (
                  <code className="xir-point" title={node.point}>{node.point}</code>
                ) : (
                  <span className="xir-nopoint">{sealed ? t("xir.sealedTag") : t("xir.noPoint")}</span>
                )}
                {dots.length ? (
                  <span className="xir-dots" aria-hidden="true">
                    {dots.slice(0, 4).map((plugin) => (
                      <i
                        key={plugin.id}
                        title={localizedName(plugin, language)}
                        style={{ background: pluginColor(plugin.id) }}
                      />
                    ))}
                    {dots.length > 4 ? <em>+{dots.length - 4}</em> : null}
                  </span>
                ) : null}
              </div>
              {card.childCount ? (
                <button
                  type="button"
                  className="xir-collapse"
                  aria-expanded={!card.collapsed}
                  aria-label={t("xir.toggleBranch", { name: localizedName(node, language) })}
                  title={t("xir.toggleBranch", { name: localizedName(node, language) })}
                  onClick={(event) => {
                    event.stopPropagation();
                    toggleCollapse(card.id);
                  }}
                >
                  {card.collapsed ? <span className="xir-collapse-count">{card.childCount}</span> : <span className="xir-collapse-minus" aria-hidden="true">−</span>}
                </button>
              ) : null}
            </div>
          );
        })}
      </div>

      <div className="xir-toolbar" role="toolbar" aria-label={t("xir.toolbarAria")}>
        <Button variant="subtle" size="icon-sm" onClick={expandAll} title={t("xir.expandAll")} aria-label={t("xir.expandAll")}>
          <ListTree size={14} aria-hidden="true" />
        </Button>
        <Button variant="subtle" size="icon-sm" onClick={collapseDefault} title={t("xir.collapseDefault")} aria-label={t("xir.collapseDefault")}>
          <Network size={14} aria-hidden="true" />
        </Button>
        <Button variant="subtle" size="icon-sm" onClick={requestFit} title={t("xir.fit")} aria-label={t("xir.fit")}>
          <Maximize2 size={14} aria-hidden="true" />
        </Button>
        <Button
          variant="subtle"
          size="icon-sm"
          className="xir-fs-toggle"
          onClick={toggleFullscreen}
          aria-pressed={isFull}
          title={isFull ? t("xir.fullscreenExit") : t("xir.fullscreenEnter")}
          aria-label={isFull ? t("xir.fullscreenExit") : t("xir.fullscreenEnter")}
        >
          {isFull ? <Shrink size={14} aria-hidden="true" /> : <Expand size={14} aria-hidden="true" />}
        </Button>
        <span className="xir-toolbar-sep" aria-hidden="true" />
        <Button variant="subtle" size="icon-sm" onClick={() => zoomButton(1 / 1.25)} title={t("xir.zoomOut")} aria-label={t("xir.zoomOut")}>
          <ZoomOut size={14} aria-hidden="true" />
        </Button>
        <span className="xir-zoom" aria-live="off">{Math.round(view.k * 100)}%</span>
        <Button variant="subtle" size="icon-sm" onClick={() => zoomButton(1.25)} title={t("xir.zoomIn")} aria-label={t("xir.zoomIn")}>
          <ZoomIn size={14} aria-hidden="true" />
        </Button>
        <span className="xir-toolbar-sep" aria-hidden="true" />
        <Button
          variant={hooksOnly ? "primary" : "subtle"}
          size="sm"
          onClick={() => {
            setHooksOnly((v) => !v);
            fitRequested.current = true;
          }}
          aria-pressed={hooksOnly}
          title={t("xir.hooksOnlyHint")}
        >
          <Filter size={13} aria-hidden="true" />
          {t("xir.hooksOnly")}
        </Button>
        <Popover>
          <PopoverTrigger asChild>
            <Button variant="subtle" size="icon-sm" title={t("xir.legend")} aria-label={t("xir.legend")}>
              <Info size={14} aria-hidden="true" />
            </Button>
          </PopoverTrigger>
          <PopoverContent className="xir-legend-pop" side="bottom" align="start">
            <h4>{t("xir.legend")}</h4>
            <div className="xir-legend-grid">
              <span className="xir-legend-swatch xir-sec-request" aria-hidden="true" />
              <span>{t("xir.section.request")}</span>
              <span className="xir-legend-swatch xir-sec-response" aria-hidden="true" />
              <span>{t("xir.section.response")}</span>
              <span className="xir-legend-swatch xir-sec-stream" aria-hidden="true" />
              <span>{t("xir.section.stream")}</span>
            </div>
            <ul className="xir-legend-list">
              <li><Lock size={12} aria-hidden="true" /> {t("xir.legendSealed")}</li>
              <li><span className="xir-legend-dot" aria-hidden="true" /> {t("xir.legendDots")}</li>
              <li><span className="xir-legend-flow" aria-hidden="true" /> {t("xir.legendFlow")}</li>
              <li><span className="xir-legend-branch" aria-hidden="true">3</span> {t("xir.legendBranch")}</li>
            </ul>
          </PopoverContent>
        </Popover>
      </div>

      <div className="xir-brand" aria-hidden="true">
        <Network size={12} strokeWidth={1.8} />
        <span>{t("xir.brand")}</span>
      </div>

      {hooksOnly && !hooked.size ? (
        <div className="xir-filter-note" role="status">{t("xir.hooksOnlyEmpty")}</div>
      ) : null}

      <div className="xir-hint" aria-hidden={false}>
        {index.representations.map((rep) => (
          <span
            key={rep.id}
            className="xir-rep-chip"
            title={language === "en" ? rep.description : rep.description_zh || rep.description}
          >
            {language === "en" ? rep.name : rep.name_zh || rep.name}
          </span>
        ))}
        <span className="xir-static-note">{t("xir.staticHint")}</span>
      </div>
    </div>
  );
}