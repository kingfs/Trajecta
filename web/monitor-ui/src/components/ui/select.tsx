import React from "react";
import * as SelectPrimitive from "@radix-ui/react-select";
import { Check, ChevronDown } from "lucide-react";
import { cn } from "../../lib/utils";

/*
 * Radix Select replaces the native <select> in the forms and settings panels,
 * on the grounds that a native popup cannot be themed, renders differently in
 * every browser, and needed two `linear-gradient` triangles to draw an arrow.
 *
 * The six list filters are deliberately still native `<select>` elements. They
 * filter a list, one of them submits with the form around it, and the platform
 * picker is the right control on a phone; a portalled listbox would trade that
 * away for theming.
 *
 * Two translation rules live here. A `<SelectItem value="">` is allowed and is
 * how "inherit" is spelled, but Radix treats an empty value as "nothing
 * selected" and shows the placeholder, so that option's label is passed as the
 * placeholder as well. And the placeholder is coloured faint, because here it
 * is often a real choice rather than an absence.
 */
export const Select = SelectPrimitive.Root;
// Radix renders the placeholder through this element and marks it with
// `data-placeholder`. Without the faint colour the placeholder is
// indistinguishable from a chosen value, which matters here because the empty
// value is a real choice in three of these selects ("inherit").
export function SelectValue({ className, ...props }: React.ComponentProps<typeof SelectPrimitive.Value>) {
  return <SelectPrimitive.Value className={cn("data-[placeholder]:text-faint", className)} {...props} />;
}
export const SelectGroup = SelectPrimitive.Group;

export function SelectTrigger({ className, children, ...props }: React.ComponentProps<typeof SelectPrimitive.Trigger>) {
  return (
    <SelectPrimitive.Trigger
      className={cn(
        // The same box as the input primitive, because a select sits next to
        // one in every form it appears in: 32px, the sunken surface, the same
        // accent ring. Width is left to the call site - `.provider-form select`
        // used to stretch it to the grid cell, and a button does not inherit
        // that rule.
        "inline-flex h-8 min-w-0 cursor-pointer items-center justify-between gap-2 rounded-md border border-border bg-secondary px-3 font-sans text-sm leading-[1.4] text-foreground outline-none transition-colors hover:border-border-strong focus:border-[color:var(--accent-solid)] focus:shadow-[0_0_0_3px_var(--accent-soft)] disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      {...props}
    >
      {children}
      <SelectPrimitive.Icon asChild>
        <ChevronDown className="size-3.5 text-faint" />
      </SelectPrimitive.Icon>
    </SelectPrimitive.Trigger>
  );
}

export function SelectContent({ className, children, position = "popper", ...props }: React.ComponentProps<typeof SelectPrimitive.Content>) {
  return (
    <SelectPrimitive.Portal>
      <SelectPrimitive.Content
        position={position}
        className={cn(
          "z-300 max-h-80 min-w-[8rem] overflow-hidden rounded-lg border border-border bg-card p-1 shadow-[var(--shadow-lg)]",
          className,
        )}
        {...props}
      >
        <SelectPrimitive.Viewport className="p-0">{children}</SelectPrimitive.Viewport>
      </SelectPrimitive.Content>
    </SelectPrimitive.Portal>
  );
}

export function SelectItem({ className, children, ...props }: React.ComponentProps<typeof SelectPrimitive.Item>) {
  return (
    <SelectPrimitive.Item
      className={cn(
        "relative flex cursor-pointer items-center gap-2 rounded-md py-2 pr-8 pl-2 font-sans text-sm text-foreground outline-none select-none data-[disabled]:pointer-events-none data-[disabled]:opacity-50 data-[highlighted]:bg-accent",
        className,
      )}
      {...props}
    >
      <SelectPrimitive.ItemText>{children}</SelectPrimitive.ItemText>
      <span className="absolute right-2 flex size-4 items-center justify-center">
        <SelectPrimitive.ItemIndicator>
          <Check className="size-3.5" />
        </SelectPrimitive.ItemIndicator>
      </span>
    </SelectPrimitive.Item>
  );
}

export function SelectLabel({ className, ...props }: React.ComponentProps<typeof SelectPrimitive.Label>) {
  return <SelectPrimitive.Label className={cn("px-2 py-1.5 font-sans text-label text-faint", className)} {...props} />;
}

export function SelectSeparator({ className, ...props }: React.ComponentProps<typeof SelectPrimitive.Separator>) {
  return <SelectPrimitive.Separator className={cn("my-1 h-px bg-border-subtle", className)} {...props} />;
}
