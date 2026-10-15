import assert from "node:assert/strict";
import { afterEach, describe, it } from "node:test";

import { copyText } from "../src/lib/clipboard.js";

/*
 * `copyText` exists because the async Clipboard API is only available on a
 * secure context: a Monitor opened over plain HTTP on a LAN address has no
 * `navigator.clipboard` at all. The button therefore has to fall back to the
 * legacy copy command, and it has to report whether anything actually reached
 * the clipboard so the caller can leave the text selected for a manual copy.
 */

const descriptors = new Map(
  ["navigator", "document"].map((name) => [name, Object.getOwnPropertyDescriptor(globalThis, name)]),
);

function setGlobal(name, value) {
  Object.defineProperty(globalThis, name, { value, configurable: true, writable: true });
}

function restoreGlobal(name) {
  const descriptor = descriptors.get(name);
  if (descriptor) {
    Object.defineProperty(globalThis, name, descriptor);
  } else {
    delete globalThis[name];
  }
}

afterEach(() => {
  restoreGlobal("navigator");
  restoreGlobal("document");
});

function fakeDocument({ copyResult = true } = {}) {
  const state = { commands: [], removed: [], selectionRestored: false, appended: 0 };
  const area = {
    value: "",
    style: {},
    setAttribute() {},
    select() {},
    setSelectionRange() {},
  };
  const selection = {
    rangeCount: 1,
    getRangeAt: () => ({ marker: true }),
    removeAllRanges() {},
    addRange: () => {
      state.selectionRestored = true;
    },
  };
  const document = {
    body: {
      appendChild: () => {
        state.appended += 1;
      },
      removeChild: (node) => state.removed.push(node),
    },
    createElement: () => area,
    getSelection: () => selection,
    execCommand: (command) => {
      state.commands.push(command);
      return copyResult;
    },
  };
  return { document, area, state };
}

describe("copyText", () => {
  it("refuses an empty value", async () => {
    setGlobal("navigator", {});
    assert.equal(await copyText(""), false);
    assert.equal(await copyText(undefined), false);
  });

  it("reports failure when there is neither a clipboard nor a document", async () => {
    setGlobal("navigator", {});
    assert.equal(await copyText("sk-secret"), false);
  });

  it("uses the async clipboard API when the context exposes it", async () => {
    const written = [];
    setGlobal("navigator", { clipboard: { writeText: async (value) => written.push(value) } });
    // No document at all: the secure path must not need one.
    assert.equal(await copyText("sk-secret"), true);
    assert.deepEqual(written, ["sk-secret"]);
  });

  it("falls back to the legacy copy command without a clipboard API", async () => {
    setGlobal("navigator", {});
    const { document, area, state } = fakeDocument();
    setGlobal("document", document);

    assert.equal(await copyText("sk-secret"), true);
    assert.equal(area.value, "sk-secret");
    assert.deepEqual(state.commands, ["copy"]);
    // The temporary field is always taken back out, and the reader's previous
    // selection is put back with it.
    assert.deepEqual(state.removed, [area]);
    assert.equal(state.selectionRestored, true);
  });

  it("falls back when the async clipboard rejects", async () => {
    setGlobal("navigator", { clipboard: { writeText: async () => { throw new Error("denied"); } } });
    const { document, state } = fakeDocument();
    setGlobal("document", document);

    assert.equal(await copyText("sk-secret"), true);
    assert.deepEqual(state.commands, ["copy"]);
  });

  it("reports failure when the legacy command is refused too", async () => {
    setGlobal("navigator", {});
    const { document } = fakeDocument({ copyResult: false });
    setGlobal("document", document);

    assert.equal(await copyText("sk-secret"), false);
  });
});
