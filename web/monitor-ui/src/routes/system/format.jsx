import React from "react";

// Shared formatting for the system page. These helpers used to be private to the
// single SystemPage module; every panel on the page needs them, and three copies
// of formatBytes would drift.

export function SystemFact({ label, value, accent = "" }) {
  return (
    <div className={`system-fact ${accent}`.trim()}>
      <span>{label}</span>
      <strong className="mono">{value === undefined || value === null || value === "" ? "—" : value}</strong>
    </div>
  );
}

// Postgres reports byte settings with unit "8kB" (shared_buffers) or "B"
// (work_mem); the raw value is only meaningful converted.
export function formatSettingBytes(setting) {
  const raw = Number(setting.raw ?? setting.value);
  if (!Number.isFinite(raw)) {
    return "";
  }
  const multiplier = setting.unit === "8kB" ? 8192 : 1;
  return formatBytes(raw * multiplier);
}

export function formatBytes(value) {
  const bytes = Number(value);
  if (!Number.isFinite(bytes)) {
    return "—";
  }
  const units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
  let scaled = Math.abs(bytes);
  let unit = 0;
  while (scaled >= 1024 && unit < units.length - 1) {
    scaled /= 1024;
    unit += 1;
  }
  const rendered = unit === 0 ? scaled.toFixed(0) : scaled.toFixed(scaled >= 100 ? 0 : scaled >= 10 ? 1 : 2);
  return `${bytes < 0 ? "-" : ""}${rendered} ${units[unit]}`;
}

export function formatRate(bytesPerSecond) {
  const rate = Number(bytesPerSecond);
  if (!Number.isFinite(rate)) {
    return "—";
  }
  return `${formatBytes(rate)}/s`;
}

export function formatCount(value) {
  const number = Number(value);
  if (!Number.isFinite(number)) {
    return "—";
  }
  return number.toLocaleString("en-US");
}

export function formatNumber(value, digits = 2) {
  const number = Number(value);
  if (!Number.isFinite(number)) {
    return "—";
  }
  return number.toFixed(digits);
}

// Ratio in [0,1] rendered as a percentage.
export function formatPercent(value) {
  const number = Number(value);
  if (!Number.isFinite(number)) {
    return "—";
  }
  return `${(number * 100).toFixed(2)} %`;
}

// Value that is already expressed in percentage points, which is how the host
// metrics endpoint reports CPU and memory usage.
export function formatPercentPoints(value, digits = 1) {
  const number = Number(value);
  if (!Number.isFinite(number)) {
    return "—";
  }
  return `${number.toFixed(digits)} %`;
}

export function formatMs(value) {
  const ms = Number(value);
  if (!Number.isFinite(ms)) {
    return "—";
  }
  if (ms >= 1000) {
    return `${(ms / 1000).toFixed(2)} s`;
  }
  if (ms >= 1) {
    return `${ms.toFixed(ms >= 100 ? 0 : 1)} ms`;
  }
  return `${(ms * 1000).toFixed(0)} µs`;
}

export function formatUptime(seconds) {
  const total = Number(seconds);
  if (!Number.isFinite(total) || total < 0) {
    return "—";
  }
  const days = Math.floor(total / 86400);
  const hours = Math.floor((total % 86400) / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  if (days > 0) {
    return `${days}d ${hours}h`;
  }
  if (hours > 0) {
    return `${hours}h ${minutes}m`;
  }
  return `${minutes}m ${Math.floor(total % 60)}s`;
}
