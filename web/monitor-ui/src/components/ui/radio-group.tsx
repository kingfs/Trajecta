import React from "react";
import * as RadioGroupPrimitive from "@radix-ui/react-radio-group";
import { Circle } from "lucide-react";
import { cn } from "../../lib/utils";

/*
 * A single choice out of several, exposed as `role="radiogroup"` with
 * `role="radio"` children rather than as a row of `aria-pressed` buttons.
 *
 * The difference is what a screen reader announces and what the arrow keys do.
 * A group of pressed buttons reads as N independent toggles, so nothing tells
 * the reader that picking one unpicks another, and the arrows do nothing. Radix
 * roves the tab stop across the group and moves the selection with the arrows,
 * which is the pattern the ARIA practices describe.
 *
 * Radix also renders a hidden native radio per item, so the group participates
 * in form submission and in the browser's own radio semantics.
 */
export function RadioGroup({ className, ...props }: React.ComponentProps<typeof RadioGroupPrimitive.Root>) {
  return <RadioGroupPrimitive.Root className={cn("grid gap-0.5", className)} {...props} />;
}

export function RadioGroupItem({
  className,
  children,
  indicator = true,
  ...props
}: React.ComponentProps<typeof RadioGroupPrimitive.Item> & {
  /** Render the filled dot. Off for items that draw their own affordance. */
  indicator?: boolean;
}) {
  return (
    <RadioGroupPrimitive.Item
      className={cn(
        "relative flex cursor-pointer appearance-none items-center gap-2 rounded-md border-0 bg-transparent py-2 pr-2 pl-8 font-sans text-sm text-foreground outline-none select-none",
        "data-[disabled]:pointer-events-none data-[disabled]:opacity-50",
        "hover:bg-accent data-[state=checked]:text-foreground focus-visible:ring-2 focus-visible:ring-ring",
        className,
      )}
      {...props}
    >
      {children}
      {indicator ? (
        <span className="pointer-events-none absolute left-2 flex size-4 items-center justify-center">
          <RadioGroupPrimitive.Indicator>
            <Circle className="size-2 fill-current" />
          </RadioGroupPrimitive.Indicator>
        </span>
      ) : null}
    </RadioGroupPrimitive.Item>
  );
}
