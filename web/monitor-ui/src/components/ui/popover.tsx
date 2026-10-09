import React from "react";
import * as PopoverPrimitive from "@radix-ui/react-popover";
import { cn } from "../../lib/utils";

/*
 * An anchored floating panel, for surface that belongs to a control rather than
 * to the page.
 *
 * The distinction from `dialog.tsx` is modality, and it is the reason both
 * exist. A dialog is a page-level interruption: it dims everything, blocks the
 * background and traps focus until it is dismissed. A popover is attached to the
 * control that opened it, so the page stays live and focus can leave by tabbing,
 * which is what a settings panel hanging off a toolbar button should do.
 *
 * The distinction from `dropdown-menu.tsx` is semantics. That one renders
 * `role="menu"` with `menuitem` children, which announces "a list of commands"
 * and promises arrow-key navigation between them; it is right for a list of
 * actions and wrong for a panel holding radio groups, where the arrows belong to
 * the group rather than to the panel.
 *
 * Radix portals the content and positions it with popper, so the panel is
 * measured against the viewport instead of against whatever the trigger happens
 * to sit inside - a rail, a scroll container, or a table cell with `overflow:
 * hidden`. It flips sides when there is no room, which is why a panel asked to
 * open upwards opens downwards on a short viewport rather than off-screen.
 */
export const Popover = PopoverPrimitive.Root;
export const PopoverTrigger = PopoverPrimitive.Trigger;
export const PopoverAnchor = PopoverPrimitive.Anchor;
export const PopoverClose = PopoverPrimitive.Close;

export function PopoverContent({
  className,
  align = "start",
  side = "bottom",
  sideOffset = 8,
  collisionPadding = 12,
  ...props
}: React.ComponentProps<typeof PopoverPrimitive.Content>) {
  return (
    <PopoverPrimitive.Portal>
      <PopoverPrimitive.Content
        align={align}
        side={side}
        sideOffset={sideOffset}
        collisionPadding={collisionPadding}
        className={cn(
          "z-300 rounded-xl border border-border bg-card p-1.5 font-sans shadow-[var(--shadow-lg)] outline-none",
          className,
        )}
        {...props}
      />
    </PopoverPrimitive.Portal>
  );
}
