import React from "react";
import { cn } from "../../lib/utils";

/*
 * The native controls the legacy sheets styled with one element rule: 32px
 * tall, a sunken surface, and an accent ring on focus. The rule is reproduced
 * here rather than replaced, because every migrated control sits next to an
 * unmigrated one and has to keep the same box.
 *
 * The base deliberately does not force a width. An input is sized by its own
 * default inside a label or a filter bar, and `w-full` would stretch forty of
 * them; call sites that want the full width ask for it, and the three filter
 * widths (180/120/260px, once `.filter-input`, `-small` and `-wide`) are
 * requested the same way.
 */
export function Input({ className, type = "text", ...props }: React.ComponentProps<"input">) {
  return (
    <input
      type={type}
      className={cn(
        "h-8 min-w-0 rounded-md border border-border bg-secondary px-3 font-sans text-sm leading-[1.4] text-foreground outline-none transition-colors placeholder:text-faint hover:border-border-strong focus:border-[color:var(--accent-solid)] focus:shadow-[0_0_0_3px_var(--accent-soft)] disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      {...props}
    />
  );
}

export function Textarea({ className, ...props }: React.ComponentProps<"textarea">) {
  return (
    <textarea
      className={cn(
        "rounded-md border border-border bg-secondary px-3 py-2 font-sans text-sm leading-[1.4] text-foreground outline-none transition-colors placeholder:text-faint hover:border-border-strong focus:border-[color:var(--accent-solid)] focus:shadow-[0_0_0_3px_var(--accent-soft)] disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      {...props}
    />
  );
}

export function Label({ className, ...props }: React.ComponentProps<"label">) {
  return <label className={cn("font-sans text-xs font-medium text-muted-foreground", className)} {...props} />;
}
