import React from "react";
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

function IconFrame({ children }) {
  return <span className="icon-frame">{children}</span>;
}

function MetricIcon({ type = "total" }) {
  if (type === "duration") {
    return (
      <svg viewBox="0 0 16 16" aria-hidden="true">
        <circle cx="8" cy="8" r="5.4" fill="none" stroke="currentColor" strokeWidth="1.3" />
        <path d="M8 4.7v3.6l2.4 1.5" fill="none" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" strokeLinejoin="round" />
      </svg>
    );
  }
  if (type === "ttft") {
    return (
      <svg viewBox="0 0 16 16" aria-hidden="true">
        <path d="M8 2.5v3.8" fill="none" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" />
        <path d="M4.6 7.2 8 3.8l3.4 3.4" fill="none" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" strokeLinejoin="round" />
        <path d="M3 9.3h10M3 12.2h7" fill="none" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" />
      </svg>
    );
  }
  if (type === "rate") {
    return (
      <svg viewBox="0 0 16 16" aria-hidden="true">
        <path d="M3 11.7a5.6 5.6 0 1 1 10 0" fill="none" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" />
        <path d="m8.2 9.2 2.9-2.9" fill="none" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" />
        <circle cx="8" cy="9.4" r="1" fill="currentColor" />
      </svg>
    );
  }
  if (type === "pp") {
    return (
      <svg viewBox="0 0 16 16" aria-hidden="true">
        <path d="M2 10V4a2 2 0 012-2h4l2 2h4a2 2 0 012 2v6a2 2 0 01-2 2H4a2 2 0 01-2-2z" fill="none" stroke="currentColor" strokeWidth="1.3" />
        <path d="M10 5v1.8" fill="none" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" />
        <path d="m8.8 6.2 1.2-1.2 1.2 1.2" fill="none" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" strokeLinejoin="round" />
      </svg>
    );
  }
  if (type === "tg") {
    return (
      <svg viewBox="0 0 16 16" aria-hidden="true">
        <path d="M3 3.5h4M3 6.5h6M3 9.5h5" fill="none" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" />
        <path d="M13 10.5V7.3" fill="none" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" />
        <path d="m11.5 8.5 1.5-1.5 1.5 1.5" fill="none" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" strokeLinejoin="round" />
      </svg>
    );
  }
  if (type === "input") {
    return (
      <svg viewBox="0 0 16 16" aria-hidden="true">
        <path d="M14 3.5h-4.5M14 12.5h-4.5M6 8H14" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
        <path d="m6.5 4.5-3.5 3.5 3.5 3.5" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" />
      </svg>
    );
  }
  if (type === "output") {
    return (
      <svg viewBox="0 0 16 16" aria-hidden="true">
        <path d="M2 3.5h4.5M2 12.5h4.5M2 8H10" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
        <path d="m9.5 4.5 3.5 3.5-3.5 3.5" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" />
      </svg>
    );
  }
  if (type === "cached") {
    return (
      <svg viewBox="0 0 16 16" aria-hidden="true">
        <path d="M5 5.5h7v7H5z" fill="none" stroke="currentColor" strokeWidth="1.3" />
        <path d="M3.5 3.5h7v7" fill="none" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" />
      </svg>
    );
  }
  if (type === "percent") {
    return (
      <svg viewBox="0 0 16 16" aria-hidden="true">
        <path d="m4 12 8-8" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
        <circle cx="5.2" cy="5.2" r="1.8" fill="none" stroke="currentColor" strokeWidth="1.3" />
        <circle cx="10.8" cy="10.8" r="1.8" fill="none" stroke="currentColor" strokeWidth="1.3" />
      </svg>
    );
  }
  if (type === "total") {
    // Sigma: the sum a "total tokens" chip reports.
    return (
      <svg viewBox="0 0 16 16" aria-hidden="true">
        <path d="M11.5 3.5H5l3.4 4.5L5 12.5h6.5" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" />
      </svg>
    );
  }
  if (type === "avg") {
    // A mean line between two extremes.
    return (
      <svg viewBox="0 0 16 16" aria-hidden="true">
        <path d="M2.5 4.5h11M2.5 11.5h11" fill="none" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" opacity="0.55" />
        <path d="M2.5 8h11" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" />
      </svg>
    );
  }
  if (type === "failed") {
    return (
      <svg viewBox="0 0 16 16" aria-hidden="true">
        <circle cx="8" cy="8" r="5.6" fill="none" stroke="currentColor" strokeWidth="1.3" />
        <path d="M8 5.2v3.6" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
        <circle cx="8" cy="11" r="0.8" fill="currentColor" />
      </svg>
    );
  }
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M3 4.5h10M3 8h10M3 11.5h10" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
    </svg>
  );
}

export function PlusIcon() {
  return (
    <IconFrame>
      <svg viewBox="0 0 24 24" aria-hidden="true">
        <path d="M12 5v14" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" />
        <path d="M5 12h14" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" />
      </svg>
    </IconFrame>
  );
}

export function EditIcon() {
  return (
    <IconFrame>
      <svg viewBox="0 0 24 24" aria-hidden="true">
        <path d="M4 20h4l10.5-10.5a2.1 2.1 0 0 0-3-3L5 17v3Z" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinejoin="round" />
        <path d="m14 8 2 2" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" />
      </svg>
    </IconFrame>
  );
}

export function DeleteIcon() {
  return (
    <IconFrame>
      <svg viewBox="0 0 24 24" aria-hidden="true">
        <path d="M5 7h14" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" />
        <path d="M10 11v6M14 11v6" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" />
        <path d="M8 7l1-3h6l1 3" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" />
        <path d="M7 7l1 14h8l1-14" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinejoin="round" />
      </svg>
    </IconFrame>
  );
}

export function ProbeIcon() {
  return (
    <IconFrame>
      <svg viewBox="0 0 24 24" aria-hidden="true">
        <circle cx="11" cy="11" r="5" fill="none" stroke="currentColor" strokeWidth="1.8" />
        <path d="m15 15 4 4" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" />
        <path d="M11 8v3l2 1.5" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" />
      </svg>
    </IconFrame>
  );
}

export function ViewIcon() {
  return (
    <IconFrame>
      <svg viewBox="0 0 24 24" aria-hidden="true">
        <path d="M2.5 12s3.4-6 9.5-6 9.5 6 9.5 6-3.4 6-9.5 6-9.5-6-9.5-6Z" fill="none" stroke="currentColor" strokeWidth="1.8" />
        <circle cx="12" cy="12" r="3.2" fill="none" stroke="currentColor" strokeWidth="1.8" />
      </svg>
    </IconFrame>
  );
}

export function DownloadIcon() {
  return (
    <IconFrame>
      <svg viewBox="0 0 24 24" aria-hidden="true">
        <path d="M12 4v10" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" />
        <path d="m8 11.5 4 4 4-4" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" />
        <path d="M5 19h14" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" />
      </svg>
    </IconFrame>
  );
}

export function HomeIcon() {
  return (
    <IconFrame>
      <svg viewBox="0 0 24 24" aria-hidden="true">
        <path d="M4 11.5 12 5l8 6.5" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" />
        <path d="M7.5 10.5V19h9v-8.5" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" />
      </svg>
    </IconFrame>
  );
}

export function StackIcon() {
  return (
    <IconFrame>
      <svg viewBox="0 0 24 24" aria-hidden="true">
        <path d="M12 4 4 8l8 4 8-4-8-4Z" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" />
        <path d="m4 12 8 4 8-4" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" />
        <path d="m4 16 8 4 8-4" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" />
      </svg>
    </IconFrame>
  );
}
