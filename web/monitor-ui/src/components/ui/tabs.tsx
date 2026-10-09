import React from "react";
import * as TabsPrimitive from "@radix-ui/react-tabs";
import { cn } from "../../lib/utils";
import { m } from "../../lib/motion";

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

/*
 * Tabs in one strip share one indicator, and `layoutId` is what makes it one
 * element: motion matches the id across the strip and animates the element from
 * where it was to where it now is. The id therefore has to be unique per strip,
 * which is what this context carries - two strips on one page would otherwise
 * hand their indicator to each other.
 */
const TabStripContext = React.createContext("trajecta-tabs");

const INDICATOR_SPRING = { type: "spring", stiffness: 380, damping: 34, restDelta: 0.001 } as const;

export function TabsList({ className, children, ...props }: React.ComponentProps<typeof TabsPrimitive.List>) {
  const strip = React.useId();
  return (
    <TabsPrimitive.List
      className={cn("mb-5 flex w-full items-end gap-1 overflow-x-auto border-b border-border scrollbar-none", className)}
      {...props}
    >
      <TabStripContext.Provider value={strip}>{children}</TabStripContext.Provider>
    </TabsPrimitive.List>
  );
}

export function TabsTrigger({
  className,
  children,
  // The strip knows which trigger is selected, and only the selected one may
  // render the shared indicator: two elements with one layoutId at the same
  // time is what the id cannot express.
  active = false,
  ...props
}: React.ComponentProps<typeof TabsPrimitive.Trigger> & { active?: boolean }) {
  const strip = React.useContext(TabStripContext);
  return (
    <TabsPrimitive.Trigger
      className={cn(
        // Every value here reproduces .tab-strip-item in layout.css, which this
        // replaces: no border, no background, no radius, and a -1px margin so
        // the indicator sits on the list's rule. The 2px bottom border stays as
        // a transparent placeholder so the box is the same height whether or
        // not the indicator is present.
        "relative -mb-px inline-flex cursor-pointer items-center gap-2 border-0 border-b-2 border-b-transparent bg-transparent px-3 py-2 font-sans text-base font-medium whitespace-nowrap text-muted-foreground transition-colors outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring data-[state=active]:text-foreground",
        className,
      )}
      {...props}
    >
      {children}
      {active ? (
        <m.span
          aria-hidden="true"
          data-tab-indicator=""
          layoutId={`tab-indicator-${strip}`}
          transition={INDICATOR_SPRING}
          className="pointer-events-none absolute inset-x-0 -bottom-px h-0.5 rounded-pill bg-brand-solid"
        />
      ) : null}
    </TabsPrimitive.Trigger>
  );
}

export function TabsContent({ className, ...props }: React.ComponentProps<typeof TabsPrimitive.Content>) {
  return <TabsPrimitive.Content className={cn("outline-none", className)} {...props} />;
}
