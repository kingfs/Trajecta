import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { traceFindingLocateURL } from "../src/lib/findingLocate.js";

describe("traceFindingLocateURL", () => {
  it("addresses the finding's locate read", () => {
    assert.equal(
      traceFindingLocateURL("trace/1", "finding_abc"),
      "/api/traces/trace%2F1/findings/finding_abc/locate",
    );
  });
});
