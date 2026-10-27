import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { traceMaskLocateURL } from "../src/lib/maskLocate.js";

describe("traceMaskLocateURL", () => {
  it("addresses the mask's locate read", () => {
    assert.equal(
      traceMaskLocateURL("trace/1", "mask-1"),
      "/api/traces/trace%2F1/secret-masks/mask-1/locate",
    );
  });
});
