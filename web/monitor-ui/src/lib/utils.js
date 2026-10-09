import { clsx } from "clsx";
import { twMerge } from "tailwind-merge";

/**
 * The class-name helper every shadcn/ui component is written against: `clsx`
 * flattens the conditional forms, `twMerge` then resolves conflicts so a caller
 * can override a variant's class ("px-4" passed in beats the primitive's own
 * "px-3"). Kept at this path and under this name so components copied from the
 * shadcn registry can be used unmodified.
 */
export function cn(...inputs) {
  return twMerge(clsx(inputs));
}
