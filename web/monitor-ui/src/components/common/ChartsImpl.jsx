import React from "react";
import {
  CartesianGrid,
  Cell,
  Legend,
  Line,
  LineChart,
  Pie,
  PieChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import { formatCount, formatTimelineBucketLabel } from "../../lib/monitor";
import { useI18n } from "../../lib/i18n";

// Series palette. It has to stay legible on both themes, so the hues are the
// desaturated cousins of the semantic colours rather than the raw tokens: the
// accent leads, then green/amber/rose/violet, then supporting teals.
const COLORS = ["#5b8cff", "#3ecf8e", "#e3a008", "#f2555a", "#a78bfa", "#22a7c4", "#e07b39", "#4f9d7a"];

export function MultiLineChart({
  items = [],
  series = [],
  metric = "request_count",
  height = 260,
  // How the axis, the tooltip and the legend spell a value. The default is a
  // plain count; a percentage or a byte total passes its own, because a chart
  // that formats everything as an integer is only right for one of them.
  valueFormatter = formatCount,
  labelFormatter = formatTimelineBucketLabel,
  yTickFormatter = valueFormatter,
}) {
  const { t } = useI18n();
  const data = buildChartData(items, series, metric, labelFormatter);
  if (!data.length || !series.length) {
    return <div className="chart-empty">{t("common.noTrend")}</div>;
  }
  return (
    <div className="line-chart-card" style={{ height }}>
      <ResponsiveContainer width="100%" height="100%">
        <LineChart data={data} margin={{ top: 10, right: 18, bottom: 0, left: 0 }}>
          <CartesianGrid stroke="var(--chart-grid)" strokeDasharray="3 3" vertical={false} />
          <XAxis dataKey="label" tick={{ fill: "var(--text-tertiary)", fontSize: 11 }} tickLine={false} axisLine={{ stroke: "var(--border)" }} />
          <YAxis tick={{ fill: "var(--text-tertiary)", fontSize: 11 }} tickLine={false} axisLine={false} tickFormatter={yTickFormatter} width={48} />
          <Tooltip content={<ChartTooltip format={valueFormatter} />} />
          <Legend wrapperStyle={{ color: "var(--text-secondary)", fontSize: 12, paddingTop: 8 }} />
          {series.map((item, index) => (
            <Line
              key={item.key}
              type="monotone"
              dataKey={item.key}
              name={item.name || item.key}
              stroke={COLORS[index % COLORS.length]}
              strokeWidth={2}
              dot={false}
              activeDot={{ r: 4 }}
              connectNulls
            />
          ))}
        </LineChart>
      </ResponsiveContainer>
    </div>
  );
}

export function SingleUsageCharts({ items = [], height = 240 }) {
  const { t } = useI18n();
  const requestSeries = [
    { key: "requests", name: "requests" },
    { key: "errors", name: "errors" },
  ];
  const tokenSeries = [{ key: "value", name: "tokens" }];
  return (
    <div className="usage-chart-grid">
      <section className="usage-chart-panel">
        <div className="breakdown-title">{t("common.requests")}</div>
        <MultiLineChart
          items={items.map((item) => ({
            time: item.time,
            series: {
              requests: { value: item.request_count },
              errors: { value: item.failed_request },
            },
          }))}
          series={requestSeries}
          metric="value"
          height={height}
        />
      </section>
      <section className="usage-chart-panel">
        <div className="breakdown-title">{t("common.tokens")}</div>
        <MultiLineChart items={items.map((item) => ({ time: item.time, value: item.total_tokens }))} series={tokenSeries} metric="value" height={height} />
      </section>
    </div>
  );
}

// A proportion of a whole, as a ring: the disk panel's question is "how much of
// this is left", and a ring answers it before any number is read. The caller
// hands over the slices and the two strings in the middle, so this stays a
// presentation component - the byte formatting and the labels belong to the page
// that knows what the slices are.
export function UsageDonut({
  slices = [],
  centerValue = "",
  centerDetail = "",
  height = 240,
  formatValue = formatCount,
}) {
  if (!slices.length) {
    return <div className="chart-empty">{centerDetail}</div>;
  }
  return (
    <div className="usage-donut" style={{ height }}>
      <ResponsiveContainer width="100%" height="100%">
        <PieChart>
          <Pie data={slices} dataKey="value" nameKey="label" innerRadius="64%" outerRadius="90%" stroke="none" isAnimationActive={false}>
            {slices.map((slice) => (
              <Cell key={slice.label} fill={slice.color} />
            ))}
          </Pie>
          <Tooltip content={<DonutTooltip formatValue={formatValue} detail={centerDetail} />} />
        </PieChart>
      </ResponsiveContainer>
      <div className="usage-donut-center">
        <strong>{centerValue}</strong>
        <span>{centerDetail}</span>
      </div>
    </div>
  );
}

function DonutTooltip({ active, payload, formatValue, detail }) {
  if (!active || !payload?.length) {
    return null;
  }
  const slice = payload[0];
  return (
    <div className="chart-tooltip">
      <strong>{slice.name}</strong>
      <span>{formatValue(Number(slice.value) || 0)}</span>
      {detail ? <span>{detail}</span> : null}
    </div>
  );
}

function buildChartData(items, series, metric, labelFormatter = formatTimelineBucketLabel) {
  return items.map((item) => {
    const row = {
      time: item.time,
      label: labelFormatter(item.time),
    };
    if (item.series && typeof item.series === "object") {
      for (const s of series) {
        row[s.key] = Number(item.series[s.key]?.[metric] || 0);
      }
    } else {
      row.value = Number(item[metric] || item.value || 0);
    }
    return row;
  });
}

function ChartTooltip({ active, payload, label, format = formatCount }) {
  if (!active || !payload?.length) {
    return null;
  }
  return (
    <div className="chart-tooltip">
      <strong>{label}</strong>
      {payload.map((item) => (
        <span key={item.dataKey}>
          <i style={{ background: item.color }} />
          {item.name}: {format(item.value)}
        </span>
      ))}
    </div>
  );
}
