import React from "react";
import {
  ArrowLeftToLine,
  ArrowRightFromLine,
  CircleAlert,
  Clock,
  ClockArrowUp,
  Download,
  Eye,
  Gauge,
  House,
  Layers,
  Layers3,
  Menu,
  Pencil,
  Percent,
  Plus,
  Radar,
  Sigma,
  Sparkles,
  Timer,
  Trash2,
  Zap,
} from "lucide-react";
import { formatDuration, formatRawDuration, formatRawNumber, formatTokenCount } from "../../lib/monitor";

export function InlineTag({ children, tone = "default" }) {
  return <span className={`inline-tag inline-tag-${tone}`}>{children}</span>;
}

/**
 * The shared read-only metric chip: an icon plus the already-formatted value.
 *
 * The human label and the unformatted value live in the tooltip instead of the
 * row, because a trace row carries up to nine of these and a visible label on
 * each one is what pushes the table past the viewport. `raw` is the author's
 * exact number, so hovering answers "how long really" without a second lookup.
 */
export function Metric({ icon = "total", label = "", value, raw = "", tone = "default", className = "" }) {
  const tooltip = [label, raw].filter((part) => part !== "" && part !== undefined && part !== null).join(" · ");
  return (
    <span className={`metric metric-${tone} ${className}`.trim()} title={tooltip}>
      <span className="metric-icon-wrap">
        <MetricIcon type={icon} />
      </span>
      <strong>{value}</strong>
    </span>
  );
}

export function MiniToken({ metric, value, raw = "", tone = "default", icon = "total" }) {
  const displayValue = typeof value === "number" ? formatTokenCount(value) : value || 0;
  const rawValue = raw || (typeof value === "number" ? formatRawNumber(value) : "");
  return <Metric icon={icon} label={metric} raw={rawValue} value={displayValue} tone={tone} className="mini-token" />;
}

export function TokenBadge({ label, value, raw = "", accent = "", icon = "total", format = "count" }) {
  // The caller states whether the number is a token count or a duration: it is
  // the only side that knows, and the icon is presentation, not a type tag.
  const isDuration = format === "duration";
  const displayValue = isDuration ? formatDuration(value) : formatTokenCount(value);
  const rawValue = raw || (isDuration ? formatRawDuration(value) : formatRawNumber(value));
  return <Metric icon={icon} label={label} raw={rawValue} value={displayValue} className={`token-badge ${accent}`} />;
}

export function LatencyMetric({ label, value, icon = "duration", raw = "", title = "" }) {
  return <Metric icon={icon} label={label} raw={raw || title} value={value} className="latency-metric" />;
}

export function DetailMetaPill({ label, value, mono = false }) {
  return (
    <span className={`detail-meta-pill ${mono ? "mono" : ""}`.trim()}>
      <span className="detail-meta-label">{label}</span>
      <strong>{value}</strong>
    </span>
  );
}

/*
 * The metric glyphs come from lucide-react rather than from the twelve ad-hoc
 * 16x16 paths that used to live here. Each one keeps the meaning its
 * predecessor was drawn to carry, so the icon still tells the reader which
 * number it belongs to:
 *
 *   duration -> Clock            total wall time
 *   ttft     -> Timer            time to first token
 *   pp       -> Zap              prefill (prompt processing) speed
 *   tg       -> Sparkles         generation speed
 *   rate     -> Gauge            generic throughput
 *   avg      -> ClockArrowUp     mean over the rows above
 *   input    -> ArrowLeftToLine  prompt tokens going in
 *   output   -> ArrowRightFromLine completion tokens coming out
 *   cached   -> Layers           the part served from cache
 *   total    -> Sigma            the sum
 *   percent  -> Percent          a ratio
 *   failed   -> CircleAlert      requests that did not complete
 *
 * `size` is 13px because that is the width the metric row was designed around;
 * the chip measurements in the smoke suite depend on it.
 */
const METRIC_ICONS = {
  duration: Clock,
  ttft: Timer,
  pp: Zap,
  tg: Sparkles,
  rate: Gauge,
  avg: ClockArrowUp,
  input: ArrowLeftToLine,
  output: ArrowRightFromLine,
  cached: Layers,
  total: Sigma,
  percent: Percent,
  failed: CircleAlert,
};

function MetricIcon({ type = "total" }) {
  const Icon = METRIC_ICONS[type] || Menu;
  return <Icon size={13} aria-hidden="true" />;
}

function IconFrame({ children }) {
  return <span className="icon-frame">{children}</span>;
}

export function PlusIcon({ size = 16 } = {}) {
  return (
    <IconFrame>
      <Plus size={size} aria-hidden="true" />
    </IconFrame>
  );
}

export function EditIcon({ size = 16 } = {}) {
  return (
    <IconFrame>
      <Pencil size={size} aria-hidden="true" />
    </IconFrame>
  );
}

export function DeleteIcon({ size = 16 } = {}) {
  return (
    <IconFrame>
      <Trash2 size={size} aria-hidden="true" />
    </IconFrame>
  );
}

export function ProbeIcon({ size = 16 } = {}) {
  return (
    <IconFrame>
      <Radar size={size} aria-hidden="true" />
    </IconFrame>
  );
}

export function ViewIcon({ size = 16 } = {}) {
  return (
    <IconFrame>
      <Eye size={size} aria-hidden="true" />
    </IconFrame>
  );
}

export function DownloadIcon({ size = 16 } = {}) {
  return (
    <IconFrame>
      <Download size={size} aria-hidden="true" />
    </IconFrame>
  );
}

export function HomeIcon({ size = 16 } = {}) {
  return (
    <IconFrame>
      <House size={size} aria-hidden="true" />
    </IconFrame>
  );
}

export function StackIcon({ size = 16 } = {}) {
  return (
    <IconFrame>
      <Layers3 size={size} aria-hidden="true" />
    </IconFrame>
  );
}
