import { useEffect, useState } from "react";

/**
 * Hold a boolean back for `delayMs`, and drop it immediately.
 *
 * A read that starts as a filter or window changes is usually answered in a few
 * milliseconds, and a mask that appeared for those would flash on every click.
 * Waiting first means only a read that is actually slow - the 30-day window, a
 * cold database - is announced. Going back to false is not delayed: the moment
 * the response lands the mask must stop claiming the page is loading.
 *
 * @param active whether the work is in flight
 * @param delayMs how long it has to stay in flight before `true` is returned
 */
export function useDelayedFlag(active: boolean, delayMs = 200) {
  const [visible, setVisible] = useState(false);

  useEffect(() => {
    if (!active) {
      setVisible(false);
      return undefined;
    }
    const timer = window.setTimeout(() => setVisible(true), delayMs);
    return () => window.clearTimeout(timer);
  }, [active, delayMs]);

  return visible;
}
