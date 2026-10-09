import React from "react";
import { cn } from "../../lib/utils";

/*
 * The surface every panel, stat tile and detail section sits on: 16px of
 * padding, a 12px radius and the raised-surface shadow, matching `.panel` and
 * `.stat-card` in the legacy sheets.
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
