import React from "react";
import { Slot } from "@radix-ui/react-slot";
import { cva } from "class-variance-authority";
import { cn } from "../../lib/utils";

/*
 * The button every new component and every migrated page uses.
 *
 * The variants are named after the classes they replace - `icon-button` and
 * `ghost-button` are 90 of the buttons in the console - and use the same token
 * values, so a migrated button is the same size and colour as the one beside
 * it that has not been migrated yet.
 *
 * `asChild` is the shadcn convention for rendering a link with button styling:
 * react-router's <Link> forwards its ref, so <Button asChild><Link/></Button>
 * works.
 */
const buttonVariants = cva(
  "inline-flex cursor-pointer items-center justify-center gap-2 whitespace-nowrap rounded-md border font-sans font-medium leading-none transition-colors outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50",
  {
    variants: {
      variant: {
        // .icon-button
        default: "border-border bg-secondary text-foreground hover:border-border-strong hover:bg-accent",
        // .primary-button
        primary: "border-transparent bg-primary text-primary-foreground hover:border-transparent hover:bg-brand-hover",
        // .ghost-button
        ghost: "border-border bg-card text-foreground hover:border-border-strong hover:bg-accent",
        subtle: "border-transparent bg-transparent text-muted-foreground hover:bg-accent hover:text-foreground",
        danger: "border-transparent bg-danger text-primary-foreground hover:border-transparent hover:opacity-90",
        link: "border-transparent bg-transparent p-0 text-brand underline-offset-4 hover:underline",
      },
      size: {
        sm: "h-7 px-2 text-xs",
        default: "h-8 px-3 text-sm",
        lg: "h-10 px-4 text-base",
        icon: "size-8 p-0",
        "icon-sm": "size-7 p-0",
      },
    },
    defaultVariants: { variant: "default", size: "default" },
  },
);

export function Button({ className, variant, size, asChild = false, type, ...props }) {
  const Component = asChild ? Slot : "button";
  return (
    <Component
      className={cn(buttonVariants({ variant, size }), className)}
      // A <button> inside a form defaults to submit, which has surprised this
      // codebase before. Callers that want a submit pass it explicitly.
      type={asChild ? undefined : (type ?? "button")}
      {...props}
    />
  );
}

export { buttonVariants };
