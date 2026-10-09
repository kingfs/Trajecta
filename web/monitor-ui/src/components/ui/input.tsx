import React from "react";
import { cn } from "../../lib/utils";

export function Input({ className, type = "text", ...props }: React.ComponentProps<"input">) {
  return (
    <input
      type={type}
      className={cn(
        "h-8 w-full min-w-0 rounded-md border border-border bg-card px-3 font-sans text-sm text-foreground outline-none transition-colors placeholder:text-faint focus-visible:border-brand-border focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50",
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
        "min-h-20 w-full rounded-md border border-border bg-card px-3 py-2 font-sans text-sm text-foreground outline-none transition-colors placeholder:text-faint focus-visible:border-brand-border focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      {...props}
    />
  );
}

export function Label({ className, ...props }: React.ComponentProps<"label">) {
  return <label className={cn("font-sans text-xs font-medium text-muted-foreground", className)} {...props} />;
}
