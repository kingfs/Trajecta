import React from "react";
import { cn } from "../../lib/utils";

/*
 * The surface a page section sits on, and the `.panel` class it replaces: 20px
 * of padding, the subtle hairline and no shadow. The 16px gap between stacked
 * sections is `[data-slot="card"]` in the legacy sheet rather than a utility
 * here, because `.page-body > .panel` cancels it for a surface sitting directly
 * under the page body and a utility would outrank that rule.
 *
 * `.stat-card` is not this: it is a flex column with a 4px gap and 16px of
 * padding, and it is still its own class.
 *
 * `as` is here because the sections this replaces are `<section>` elements, and
 * a surface is not a reason to demote them to a `div`. It renders the same tag
 * with the same classes, so the box and the document outline both survive.
 */
export function Card({ className, as, ...props }: React.ComponentProps<"div"> & { as?: React.ElementType }) {
  const Tag: React.ElementType = as || "div";
  return <Tag data-slot="card" className={cn("rounded-lg border border-border-subtle bg-card p-5", className)} {...props} />;
}

export function CardHeader({ className, ...props }: React.ComponentProps<"div">) {
  return <div className={cn("mb-3 flex items-start justify-between gap-3", className)} {...props} />;
}

export function CardTitle({ className, ...props }: React.ComponentProps<"h3">) {
  return <h3 className={cn("m-0 font-sans text-md font-semibold text-foreground", className)} {...props} />;
}

export function CardDescription({ className, ...props }: React.ComponentProps<"p">) {
  return <p className={cn("m-0 font-sans text-xs text-muted-foreground", className)} {...props} />;
}

export function CardContent({ className, ...props }: React.ComponentProps<"div">) {
  return <div className={cn("font-sans text-sm text-foreground", className)} {...props} />;
}
