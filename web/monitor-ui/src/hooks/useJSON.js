import { startTransition, useEffect, useState } from "react";
import { requestJSON } from "../lib/api";

export function useJSON(url, deps = []) {
  // A falsy url parks the hook: nothing is requested and the state stays idle.
  // Callers use it to wait for a parent resource before firing sub-resources.
  const enabled = typeof url === "string" && url !== "";
  const [state, setState] = useState({ loading: enabled, data: null, error: "" });
  const requestKey = JSON.stringify([url, ...deps]);

  useEffect(() => {
    if (!enabled) {
      setState({ loading: false, data: null, error: "" });
      return undefined;
    }
    let cancelled = false;
    const controller = new AbortController();
    const requestURL = url;

    startTransition(() => {
      setState((current) => ({ ...current, loading: true, error: "" }));
    });

    requestJSON(requestURL, { signal: controller.signal })
      .then((data) => {
        if (cancelled) {
          return;
        }
        startTransition(() => {
          setState({ loading: false, data, error: "" });
        });
      })
      .catch((error) => {
        if (cancelled || error.name === "AbortError") {
          return;
        }
        startTransition(() => {
          setState({ loading: false, data: null, error: error.message || "unknown error" });
        });
      });

    return () => {
      cancelled = true;
      controller.abort();
    };
  }, [requestKey, url, enabled]);

  return state;
}
