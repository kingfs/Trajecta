import React from "react";
import * as TabsPrimitive from "@radix-ui/react-tabs";
import { cn } from "../../lib/utils";

/*
 * The page-level section switch.
 *
 * The hand-written TabStrip looked correct - role="tablist", role="tab",
 * aria-selected, a roving tabindex - but implemented none of the keyboard
 * behaviour that goes with it. Arrow keys did nothing, so with only the active
 * tab focusable, every other tab on the page was unreachable without a mouse.
 * Radix implements the full pattern, and keeps the URL-driven value contract
 * the console relies on: TabbedPage passes `value` and `onValueChange`, so
 * every tab is still a linkable `?tab=`.
 */
export const Tabs = TabsPrimitive.Root;

export function TabsList({ className, ...props }) {
  return (
    <TabsPrimitive.List
      className={cn("mb-5 flex w-full items-end gap-1 overflow-x-auto border-b border-border scrollbar-none", className)}
      {...props}
    />
  );
}

export function TabsTrigger({ className, ...props }) {
  return (
    <TabsPrimitive.Trigger
      className={cn(
        // Every value here reproduces .tab-strip-item in layout.css, which this
        // replaces: no border except the 2px underline, no background, no
        // radius, and a -1px margin so the underline sits on the list's rule.
        "relative -mb-px inline-flex cursor-pointer items-center gap-2 border-0 border-b-2 border-b-transparent bg-transparent px-3 py-2 font-sans text-base font-medium whitespace-nowrap text-muted-foreground transition-colors outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring data-[state=active]:border-b-brand data-[state=active]:text-foreground",
        className,
      )}
      {...props}
    />
  );
}

export function TabsContent({ className, ...props }) {
  return <TabsPrimitive.Content className={cn("outline-none", className)} {...props} />;
}
