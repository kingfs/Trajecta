import React from "react";
import { cn } from "../../lib/utils";

/*
 * A bordered surface: 16px of padding, a 12px radius, the subtle hairline and
 * the small shadow. It is deliberately not a copy of `.panel` or `.stat-card`,
 * which are the legacy surfaces this would replace - `.panel` pads to 20px and
 * carries a 16px bottom margin, `.stat-card` is a flex column with a 4px gap,
 * and both set `box-shadow: none` through the shared rule. Those two are
 * migrated by pinning their measured boxes first, not by assuming this
 * primitive is already them.
 */
export function Card({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      className={cn("rounded-lg border border-border-subtle bg-card p-4 shadow-[var(--shadow-sm)]", className)}
      {...props}
    />
  );
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
