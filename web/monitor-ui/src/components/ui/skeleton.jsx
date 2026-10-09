import { cn } from "../../lib/utils";

/**
 * shadcn/ui's Skeleton. It is the first primitive in the Monitor written with
 * Tailwind utilities instead of a hand-written class in components.css, which
 * relies on tailwind.css importing every earlier sheet into a `legacy` layer
 * below `utilities`.
 */
export function Skeleton({ className, ...props }) {
  return <div aria-hidden="true" className={cn("animate-pulse rounded-md bg-muted", className)} {...props} />;
}
