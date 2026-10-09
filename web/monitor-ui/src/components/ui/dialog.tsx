import React from "react";
import { Button } from "./button";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import { cn } from "../../lib/utils";
import { useI18n } from "../../lib/i18n";

/*
 * Replaces the hand-rolled `.nav-modal-backdrop` + `role="dialog"` markup that
 * was copy-pasted into five places. Each copy handled Escape and the backdrop
 * click itself, and none of them did the rest: focus was never moved into the
 * dialog or returned to the trigger that opened it, Tab could walk out of the
 * dialog into the page behind it, the page behind kept scrolling, and the
 * background stayed reachable by a screen reader. Radix does all of it.
 *
 * The chrome stays the existing `.nav-modal` rules. Radix's parts are unstyled
 * on purpose, so reusing the class keeps a migrated dialog indistinguishable
 * from one that has not been migrated, which is what lets this happen one
 * dialog at a time.
 *
 * One structural difference from the old markup is unavoidable: Radix renders
 * the overlay and the content as siblings in a portal rather than nesting the
 * card inside the backdrop, so the card centres itself instead of inheriting
 * `place-items: center` from the backdrop.
 */
export const Dialog = DialogPrimitive.Root;
export const DialogTrigger = DialogPrimitive.Trigger;
export const DialogClose = DialogPrimitive.Close;
export const DialogPortal = DialogPrimitive.Portal;

type DialogContentProps = React.ComponentProps<typeof DialogPrimitive.Content> & {
  /** Render the corner close button the legacy modals all had. */
  showClose?: boolean;
  closeLabel?: string;
};

export function DialogContent({ className, children, showClose = false, closeLabel, ...props }: DialogContentProps) {
  const { t } = useI18n();
  return (
    <DialogPrimitive.Portal>
      <DialogPrimitive.Overlay className="nav-modal-backdrop" />
      <DialogPrimitive.Content
        className={cn(
          "nav-modal fixed top-1/2 left-1/2 z-[201] -translate-x-1/2 -translate-y-1/2 outline-none",
          className,
        )}
        {...props}
      >
        {children}
        {showClose ? (
          <DialogPrimitive.Close asChild>
            <Button variant="default" size="icon" aria-label={closeLabel || t("common.close")} className="absolute top-4 right-4">
              <X size={14} aria-hidden="true" />
            </Button>
          </DialogPrimitive.Close>
        ) : null}
      </DialogPrimitive.Content>
    </DialogPrimitive.Portal>
  );
}

export function DialogTitle({ className, ...props }: React.ComponentProps<typeof DialogPrimitive.Title>) {
  return <DialogPrimitive.Title className={cn("m-0 font-sans text-lg font-semibold text-foreground", className)} {...props} />;
}

export function DialogDescription({ className, ...props }: React.ComponentProps<typeof DialogPrimitive.Description>) {
  return (
    <DialogPrimitive.Description
      className={cn("m-0 mt-1 font-sans text-sm text-muted-foreground", className)}
      {...props}
    />
  );
}

export function DialogHeader({ className, ...props }: React.ComponentProps<"div">) {
  return <div className={cn("nav-modal-head", className)} {...props} />;
}

export function DialogFooter({ className, ...props }: React.ComponentProps<"div">) {
  return <div className={cn("nav-modal-actions", className)} {...props} />;
}
