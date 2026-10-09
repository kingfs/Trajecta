import React from "react";
import { MONITOR_WINDOW_OPTIONS } from "../../lib/monitor";
import { useI18n } from "../../lib/i18n";
import { TabsContent, TabsList, TabsTrigger, Tabs } from "../ui/tabs";
import { SegmentedControl, SegmentedControlItem } from "../ui/segmented-control";

/*
 * TabStrip is the page-level section switch: one row of underline tabs bound to
 * the `?tab=` query parameter by TabbedPage, so every panel of a page is a
 * linkable URL rather than a piece of component state.
 *
 * It is a thin adapter over the Radix Tabs primitive now. The hand-written
 * version had the right ARIA attributes but none of the keyboard behaviour they
 * imply: with only the active tab focusable and no arrow-key handling, every
 * other tab was unreachable from the keyboard. Radix implements the pattern.
 *
 * The controlled `value`/`onValueChange` pair is what keeps the URL the source
 * of truth; `Tabs` holds no state of its own here.
 */
export function TabStrip({ tabs, activeId, onSelect, label }) {
  return (
    <TabsList aria-label={label}>
      {tabs.map((tab) => (
        <TabsTrigger
          key={tab.id}
          value={tab.id}
          active={tab.id === activeId}
          onClick={() => onSelect(tab.id)}
        >
          <span>{tab.label}</span>
          {tab.count === undefined || tab.count === null ? null : (
            <span className="rounded-pill bg-accent px-1.5 text-label leading-4 font-medium text-muted-foreground">
              {tab.count}
            </span>
          )}
        </TabsTrigger>
      ))}
    </TabsList>
  );
}

export { Tabs, TabsContent };

// WindowToggle is the shared time-window picker: one of today, 7d, 30d or all.
//
// It takes its labels from the dictionary. Every analytics page used to print
// the raw option value ("today", "7d"), which meant the same control read
// differently from the rest of the Chinese UI, and five of them still did until
// this picker was the only one left.
export function WindowToggle({ value, onChange, label }) {
  const { t } = useI18n();
  return (
    <SegmentedControl value={value} onValueChange={onChange} aria-label={label || t("common.windowLabel")}>
      {MONITOR_WINDOW_OPTIONS.map((option) => (
        <SegmentedControlItem key={option} value={option} active={value === option}>
          {t(`common.window.${option}`)}
        </SegmentedControlItem>
      ))}
    </SegmentedControl>
  );
}
