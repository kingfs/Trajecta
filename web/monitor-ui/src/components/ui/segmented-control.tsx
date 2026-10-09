import React from "react";
import * as RadioGroupPrimitive from "@radix-ui/react-radio-group";
import { cn } from "../../lib/utils";

/*
 * A segmented control: one choice out of a few, where the choices are a filter
 * or a view rather than a section of the page.
 *
 * These strips used to declare `role="tablist"` over a row of plain buttons.
 * Nothing about them was a tab widget - no `role="tab"`, no `aria-selected`, no
 * tabpanel, and none of the keyboard behaviour a tablist promises - so a screen
 * reader announced a tab list whose items it could not describe. What they
 * really are is one choice out of a few, which is a radio group: arrow keys
 * move between the options and select as they go, and the option in force is
 * the one carrying `aria-checked`.
 *
 * Radix's radio group implements that whole pattern, and the console already
 * depends on it for the account panel's theme and language pickers, so the two
 * single-choice controls in the app are now the same control.
 *
 * The strips are laid out horizontally, so the arrow keys that move within them
 * are the horizontal pair; Radix reads that from `orientation`, which is what
 * styles cannot say.
 *
 * The knobs and switches on the page are the `Switch` primitive. Page-level
 * *sections* are `Tabs`.
 */
const HORIZONTAL_STEP: Record<string, number> = { ArrowRight: 1, ArrowLeft: -1 };
const VERTICAL_STEP: Record<string, number> = { ArrowDown: 1, ArrowUp: -1 };

export function SegmentedControl({
  className,
  orientation = "horizontal",
  children,
  onKeyDown,
  ...props
}: React.ComponentProps<typeof RadioGroupPrimitive.Root>) {
  /*
   * Arrow keys are supposed to move between the options and select as they go.
   * Radix moves the focus but not the selection, and the reason is entirely
   * about event order: it notices the arrow key in a `keydown` listener on
   * `document` (bubble phase), while React 19 dispatches from the root element,
   * which is inside `document`. Radix's flag is therefore still false when its
   * own handler moves the focus, the option that gains focus is never clicked,
   * and the key ends up doing half of what the pattern promises.
   *
   * The missing half is the selection, so that is what this adds: the option
   * next to the checked one, wrapping at the ends the way Radix's roving focus
   * does. Radix still moves the focus, and the two agree, because the option
   * that becomes checked is the one it was moving to.
   */
  const handleKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    onKeyDown?.(event);
    // Radix calls preventDefault on the arrows it handles, so this runs anyway:
    // it is not the key being unhandled, it is the missing half of handling it.
    const step = (orientation === "vertical" ? VERTICAL_STEP : HORIZONTAL_STEP)[event.key];
    if (!step) {
      return;
    }
    const options = Array.from(event.currentTarget.querySelectorAll<HTMLElement>('[role="radio"]:not([data-disabled])'));
    const checked = options.findIndex((option) => option.getAttribute("aria-checked") === "true");
    options[(checked + step + options.length) % options.length]?.click();
  };

  return (
    <RadioGroupPrimitive.Root
      orientation={orientation}
      className={cn("view-toggle", className)}
      onKeyDown={handleKeyDown}
      {...props}
    >
      {children}
    </RadioGroupPrimitive.Root>
  );
}

export function SegmentedControlItem({
  className,
  // The root owns which value is selected and Radix stamps `data-state` onto
  // each item from it, so the selected look follows the same state as the ARIA
  // attribute rather than a second copy of it. The class is spelled out because
  // the console styles these buttons by class, not by attribute.
  active = false,
  children,
  ...props
}: React.ComponentProps<typeof RadioGroupPrimitive.Item> & { active?: boolean }) {
  return (
    <RadioGroupPrimitive.Item className={cn(active ? "ghost-button active" : "ghost-button", className)} {...props}>
      {children}
    </RadioGroupPrimitive.Item>
  );
}
