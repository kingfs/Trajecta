import React from "react";
import { Switch as SwitchPrimitive } from "../ui/switch";

/**
 * The enabled/disabled toggle used across the provider, model and access pages.
 *
 * The public props are unchanged, so the seven call sites did not move, but the
 * implementation is now the Radix switch primitive: a real `role="switch"` with
 * Space and Enter both toggling it, instead of a `<button>` that had to add the
 * keyboard handling itself.
 *
 * The wrapper element exists for one reason. These switches sit inside cards
 * that are react-router `<Link>`s, so a click on the switch must not navigate.
 * The hand-written version called `preventDefault` and `stopPropagation` in its
 * own onClick. Radix cannot be told that on the switch itself: it composes its
 * internal click handler with `checkForDefaultPrevented`, so a caller that
 * calls `preventDefault` first stops the toggle from happening at all. Handling
 * the click on a parent instead puts it after Radix has toggled, at which point
 * cancelling the anchor's default is exactly right.
 *
 * `display: contents` keeps that parent out of the layout so the switch's box,
 * and the flex row it sits in, are unchanged.
 *
 * `onChange` is still called with the next value rather than an event, and the
 * label still becomes the accessible name.
 */
export function Switch({ checked, onChange, disabled = false, label = "", title = "" }) {
  return (
    <span
      style={{ display: "contents" }}
      onClick={(event) => {
        event.preventDefault();
        event.stopPropagation();
      }}
    >
      <SwitchPrimitive
        checked={Boolean(checked)}
        disabled={disabled}
        aria-label={label || (checked ? "Enabled" : "Disabled")}
        title={title || label}
        onCheckedChange={(next) => onChange?.(next)}
      />
    </span>
  );
}
