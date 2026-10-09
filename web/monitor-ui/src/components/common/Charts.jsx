import React, { Suspense, lazy } from "react";
import { Skeleton } from "../ui/skeleton";

/*
 * The charts are the single heaviest dependency in the Monitor: recharts plus
 * the store, selector and d3 packages it pulls in is around 315 kB of the
 * bundle, a third of it, and it is only needed on the four pages that draw a
 * trend line. Both wrappers below point at the same module, so the browser
 * fetches and parses that chunk once, on the first page that needs it.
 *
 * The public surface is unchanged - callers still import MultiLineChart and
 * SingleUsageCharts from here - so no page had to learn about the split.
 */
const MultiLineChartImpl = lazy(() => import("./ChartsImpl").then((module) => ({ default: module.MultiLineChart })));
const SingleUsageChartsImpl = lazy(() => import("./ChartsImpl").then((module) => ({ default: module.SingleUsageCharts })));

// Sized like the real thing so resolving the chunk does not move the page.
function ChartFallback({ height, panels = 1 }) {
  return (
    <div className="grid gap-4" style={{ gridTemplateColumns: `repeat(${panels}, minmax(0, 1fr))` }}>
      {Array.from({ length: panels }, (_, index) => (
        <div key={index} className="rounded-lg border border-border-subtle bg-card p-4">
          <Skeleton className="w-full" style={{ height: height - 32 }} />
        </div>
      ))}
    </div>
  );
}

export function MultiLineChart({ height = 260, ...props }) {
  return (
    <Suspense fallback={<ChartFallback height={height} />}>
      <MultiLineChartImpl height={height} {...props} />
    </Suspense>
  );
}

export function SingleUsageCharts({ height = 240, ...props }) {
  return (
    <Suspense fallback={<ChartFallback height={height} panels={2} />}>
      <SingleUsageChartsImpl height={height} {...props} />
    </Suspense>
  );
}
