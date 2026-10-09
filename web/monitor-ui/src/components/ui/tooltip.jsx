import React from "react";
import * as TooltipPrimitive from "@radix-ui/react-tooltip";
import { cn } from "../../lib/utils";

/*
 * An accessible replacement for the `title` attribute. `title` is not announced
 * on keyboard focus, cannot be styled, and disappears after a browser-dependent
 * delay; Radix's tooltip is described by aria-describedby and appears for focus
 * as well as hover.
 *
 * The metric chips deliberately keep `title`: a hover on one has to answer "how
 * long really" without a portal, and the chip's tooltip text is also its
 * accessible name, which several tests assert on.
 */
export const TooltipProvider = TooltipPrimitive.Provider;
export const Tooltip = TooltipPrimitive.Root;
export const TooltipTrigger = TooltipPrimitive.Trigger;

export function TooltipContent({ className, sideOffset = 6, ...props }) {
  return (
    <TooltipPrimitive.Portal>
      <TooltipPrimitive.Content
        sideOffset={sideOffset}
        className={cn(
          "z-300 max-w-80 rounded-md border border-border bg-popover px-2 py-1 font-sans text-xs text-popover-foreground shadow-[var(--shadow-lg)]",
          className,
        )}
        {...props}
      />
    </TooltipPrimitive.Portal>
  );
}
