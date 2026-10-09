import React from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "../../lib/utils";

/*
 * The pill that every status, provider and model tag in the console renders as.
 * `.inline-tag` in the legacy sheets is the same shape at 20px tall; the tones
 * below are the ones the pages actually use.
 */
const badgeVariants = cva(
  "inline-flex items-center gap-1 whitespace-nowrap rounded-pill border px-2 text-label font-medium leading-5",
  {
    variants: {
      tone: {
        default: "border-border bg-card text-muted-foreground",
        brand: "border-brand-border bg-brand-soft text-brand",
        success: "border-success-border bg-success-soft text-success",
        warning: "border-warning-border bg-warning-soft text-warning",
        danger: "border-danger-border bg-danger-soft text-danger",
        info: "border-info-border bg-info-soft text-info",
        violet: "border-violet-border bg-violet-soft text-violet",
        muted: "border-border-subtle bg-secondary text-faint",
      },
    },
    defaultVariants: { tone: "default" },
  },
);

export function Badge({ className, tone, ...props }: React.ComponentProps<"span"> & VariantProps<typeof badgeVariants>) {
  return <span className={cn(badgeVariants({ tone }), className)} {...props} />;
}

export { badgeVariants };
