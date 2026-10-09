import React from "react";
import { LazyMotion, MotionConfig, m } from "motion/react";

/*
 * The one place `motion` is imported.
 *
 * Two decisions are recorded here rather than at each use.
 *
 * `m` rather than `motion.div`: `m` is the component that does not bundle an
 * animation feature set of its own, which is what makes the DOM animation code
 * reachable through `LazyMotion` alone. Using `motion.*` anywhere pulls the full
 * feature set into the entry chunk regardless.
 *
 * The feature set is fetched rather than imported, so it lands in its own chunk
 * and the first paint does not wait for it. Until it arrives a `m` element is a
 * plain element: the tab indicator is in the right place from the first render,
 * it just moves there instantly.
 *
 * `reducedMotion="user"` honours the operating system's "reduce motion" setting
 * for anything driven from JavaScript. `prefersReducedMotion` in the CSS sheets
 * covers the keyframe animations; this covers the layout animations, which CSS
 * cannot reach.
 *
 * Motion is used for the one thing CSS cannot express here: an element that
 * moves *between* two parents as the selection changes, which is what the tab
 * indicator is. The dialog's enter and exit are keyframes, because Radix already
 * publishes `data-state` and keeps the element mounted until its animation ends.
 */
const loadFeatures = () => import("./motionFeatures").then((module) => module.default);

export function MotionProvider({ children }) {
  return (
    <MotionConfig reducedMotion="user">
      <LazyMotion features={loadFeatures} strict>
        {children}
      </LazyMotion>
    </MotionConfig>
  );
}

export { m };
