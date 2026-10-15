/*
 * The event feed says a credential was replaced; the audit table says by what.
 * These helpers are the join between the two, and they are the only thing that
 * decides where the console looks for the pair, so they are covered directly
 * rather than through a rendered page.
 */
import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
  credentialMaskItems,
  credentialMaskURLForEvent,
  credentialMaskURLForTrace,
  isCredentialMaskedEvent,
  parseEventDetails,
  sortCredentialMasks,
} from "../src/lib/credentialMasks.js";

const maskedEvent = {
  id: "evt-1",
  category: "credential_masked",
  source: "secretmask",
  trace_id: "trace-1",
  details_json: JSON.stringify({ rule: "openai.project_key", length: 132, trace_id: "trace-1" }),
};

describe("recognizing the masking notification", () => {
  it("accepts the secret-masking event", () => {
    assert.equal(isCredentialMaskedEvent(maskedEvent), true);
  });

  it("rejects another source using the same category", () => {
    assert.equal(isCredentialMaskedEvent({ ...maskedEvent, source: "analyzer" }), false);
  });

  it("rejects another category from the same source", () => {
    assert.equal(isCredentialMaskedEvent({ ...maskedEvent, category: "parse_failure" }), false);
  });

  it("survives a missing event", () => {
    assert.equal(isCredentialMaskedEvent(undefined), false);
  });
});

describe("parsing details_json", () => {
  it("reads the object an event carries", () => {
    assert.deepEqual(parseEventDetails(maskedEvent), { rule: "openai.project_key", length: 132, trace_id: "trace-1" });
  });

  it("reads a string payload", () => {
    assert.deepEqual(parseEventDetails({ details_json: '{"rule":"jwt"}' }), { rule: "jwt" });
  });

  it("returns null for absent or unparsable details", () => {
    assert.equal(parseEventDetails({}), null);
    assert.equal(parseEventDetails({ details_json: "not json" }), null);
    // A JSON scalar parses but has no rule in it, and reading `rule` off a
    // string is not something the callers should have to guard.
    assert.equal(parseEventDetails({ details_json: "7" }), null);
  });
});

describe("the audit query an event resolves to", () => {
  it("scopes the read to the trace and the rule", () => {
    assert.equal(credentialMaskURLForEvent(maskedEvent), "/api/secret-masks?trace_id=trace-1&page_size=50&rule=openai.project_key");
  });

  it("falls back to the event's trace when the details carry none", () => {
    const event = { ...maskedEvent, details_json: JSON.stringify({ rule: "jwt" }) };
    assert.equal(credentialMaskURLForEvent(event), "/api/secret-masks?trace_id=trace-1&page_size=50&rule=jwt");
  });

  it("still reads the trace when no rule is recorded", () => {
    const event = { ...maskedEvent, details_json: "{}" };
    assert.equal(credentialMaskURLForEvent(event), "/api/secret-masks?trace_id=trace-1&page_size=50");
  });

  it("parks the read for an unrelated event", () => {
    assert.equal(credentialMaskURLForEvent({ ...maskedEvent, category: "parse_failure" }), null);
  });

  it("parks the read when nothing names a trace", () => {
    assert.equal(credentialMaskURLForEvent({ ...maskedEvent, trace_id: "", details_json: "{}" }), null);
  });

  it("escapes the trace in the trace-scoped read", () => {
    assert.equal(credentialMaskURLForTrace("a/b"), "/api/secret-masks?trace_id=a%2Fb&page_size=50");
    assert.equal(credentialMaskURLForTrace(""), null);
  });
});

describe("ordering the replacements", () => {
  it("prints them in the order the request body carried them", () => {
    const rows = [
      { rule: "openai.project_key", event_index: 2, start: 900 },
      { rule: "google.api_key", event_index: 0, start: 300 },
      { rule: "jwt", event_index: 1, start: 120 },
    ];
    assert.deepEqual(sortCredentialMasks(rows).map((row) => row.rule), ["google.api_key", "jwt", "openai.project_key"]);
  });

  it("falls back to the offset when two rows share an index", () => {
    const rows = [
      { rule: "b", event_index: 0, start: 40 },
      { rule: "a", event_index: 0, start: 10 },
    ];
    assert.deepEqual(sortCredentialMasks(rows).map((row) => row.rule), ["a", "b"]);
  });

  it("does not reorder the caller's array", () => {
    const rows = [{ event_index: 1 }, { event_index: 0 }];
    const sorted = sortCredentialMasks(rows);
    assert.equal(rows[0].event_index, 1);
    assert.equal(sorted[0].event_index, 0);
  });

  it("treats a missing list as empty", () => {
    assert.deepEqual(sortCredentialMasks(undefined), []);
  });
});

describe("reading the page", () => {
  it("takes the rows out of the paged response", () => {
    assert.deepEqual(credentialMaskItems({ items: [{ id: "a" }], total: 1 }), [{ id: "a" }]);
  });

  it("tolerates a bare list and an empty body", () => {
    assert.deepEqual(credentialMaskItems([{ id: "a" }]), [{ id: "a" }]);
    assert.deepEqual(credentialMaskItems(null), []);
    assert.deepEqual(credentialMaskItems({ total: 0 }), []);
  });
});
