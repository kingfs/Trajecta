/*
 * The QueryClient defaults are deliberate and each one replaces a behaviour the
 * hand-written useJSON hook had. They are asserted here because a change to any
 * of them shows up as a slow, retrying or flickering console rather than as a
 * failure, which is exactly the kind of regression that goes unnoticed.
 */
import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { createQueryClient } from "../src/lib/queryClient.ts";

describe("query client defaults", () => {
  it("does not retry, so a failure surfaces once", () => {
    const client = createQueryClient();
    assert.equal(client.getDefaultOptions().queries.retry, false);
  });

  it("does not refetch on window focus", () => {
    const client = createQueryClient();
    assert.equal(client.getDefaultOptions().queries.refetchOnWindowFocus, false);
  });

  it("deduplicates requests made within the stale window", () => {
    const client = createQueryClient();
    const { staleTime, gcTime } = client.getDefaultOptions().queries;
    assert.ok(staleTime > 0, "staleTime must be positive or every mount refetches");
    assert.ok(gcTime > staleTime, "cache must outlive freshness or remounting blanks the page");
  });
});
