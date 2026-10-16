import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
  countSchemaParameters,
  parseSchemaDocument,
  schemaAnnotations,
  schemaItems,
  schemaPropertyRows,
  schemaTypeLabel,
} from "../src/lib/jsonSchema.js";

/*
 * The definition dialog draws a tool's declaration as a form. What it can draw
 * is what these helpers can read out of a schema, and a declaration reaches the
 * dialog in four shapes: the OpenAI wrapper, a flat tool, an Anthropic
 * `input_schema`, and the JSON text of any of them. A shape the helpers miss is
 * a tool whose parameters render as empty.
 */
describe("parseSchemaDocument", () => {
  const schema = { type: "object", properties: { cmd: { type: "string" } } };

  it("unwraps the shapes a declaration arrives in", () => {
    assert.deepEqual(parseSchemaDocument({ type: "function", function: { name: "exec", parameters: schema } }), schema);
    assert.deepEqual(parseSchemaDocument({ name: "exec", parameters: schema }), schema);
    assert.deepEqual(parseSchemaDocument({ name: "exec", input_schema: schema }), schema);
    assert.deepEqual(parseSchemaDocument(JSON.stringify(schema)), schema);
    assert.deepEqual(parseSchemaDocument(schema), schema);
  });

  it("leaves a schema that has a property called parameters alone", () => {
    const withProperty = { type: "object", properties: { parameters: { type: "string" } } };
    assert.deepEqual(parseSchemaDocument(withProperty), withProperty);
  });

  it("answers nothing for a declaration it cannot read", () => {
    assert.equal(parseSchemaDocument("{broken"), null);
    assert.equal(parseSchemaDocument(""), null);
    assert.equal(parseSchemaDocument(null), null);
    assert.equal(parseSchemaDocument("not json"), null);
  });
});

describe("schemaPropertyRows", () => {
  it("keeps the declared order and takes requiredness from the parent", () => {
    const rows = schemaPropertyRows({
      type: "object",
      required: ["cwd"],
      properties: { cmd: { type: "string" }, cwd: { type: "string" } },
    });
    assert.deepEqual(rows.map((row) => [row.name, row.required]), [["cmd", false], ["cwd", true]]);
  });

  it("answers nothing for a schema without properties", () => {
    assert.deepEqual(schemaPropertyRows({ type: "array", items: { type: "string" } }), []);
    assert.deepEqual(schemaPropertyRows(null), []);
  });
});

describe("schemaTypeLabel", () => {
  it("names the type a parameter accepts", () => {
    assert.equal(schemaTypeLabel({ type: "string" }), "string");
    assert.equal(schemaTypeLabel({ type: ["string", "null"] }), "string | null");
    assert.equal(schemaTypeLabel({ type: "object", properties: {} }), "object");
    assert.equal(schemaTypeLabel({ items: { type: "string" } }), "array");
    assert.equal(schemaTypeLabel({ $ref: "#/$defs/Path" }), "Path");
    assert.equal(schemaTypeLabel({ anyOf: [{ type: "string" }, { type: "number" }] }), "anyOf of 2");
    // A schema that declares no type at all is not "string" and not a crash.
    assert.equal(schemaTypeLabel({ description: "anything" }), "any");
    assert.equal(schemaTypeLabel(null), "any");
  });

  it("reads an enumeration with no type of its own", () => {
    assert.equal(schemaTypeLabel({ enum: ["a", "b"] }), "string (enum)");
    assert.equal(schemaTypeLabel({ enum: ["a", 1] }), "enum");
  });
});

describe("schemaAnnotations", () => {
  it("collects what the form shows beside the type", () => {
    const annotations = schemaAnnotations({ enum: ["ls", "pwd"], default: "ls", format: "hostname", pattern: "^a", minimum: 1, maximum: 9, additionalProperties: false });
    assert.deepEqual(annotations.values, ["ls", "pwd"]);
    assert.equal(annotations.hasDefault, true);
    assert.equal(annotations.defaultValue, "ls");
    assert.equal(annotations.format, "hostname");
    assert.equal(annotations.pattern, "^a");
    assert.equal(annotations.minimum, 1);
    assert.equal(annotations.maximum, 9);
    assert.equal(annotations.additionalProperties, false);
  });

  it("does not invent a default for a schema without one", () => {
    assert.equal(schemaAnnotations({ type: "string" }).hasDefault, false);
    assert.equal(schemaAnnotations(null).values.length, 0);
  });
});

describe("schemaItems and countSchemaParameters", () => {
  it("reads the element schema of an array", () => {
    assert.deepEqual(schemaItems({ type: "array", items: { type: "string" } }), { type: "string" });
    assert.deepEqual(schemaItems({ type: "array", items: [{ type: "string" }] }), { type: "string" });
    assert.equal(schemaItems({ type: "string" }), null);
  });

  it("counts nested parameters, not just the top level", () => {
    const schema = {
      type: "object",
      properties: {
        cmd: { type: "string" },
        options: { type: "object", properties: { cwd: { type: "string" }, env: { type: "object", properties: { PATH: { type: "string" } } } } },
        args: { type: "array", items: { type: "object", properties: { value: { type: "string" } } } },
      },
    };
    // cmd, options, cwd, env, PATH, args, value
    assert.equal(countSchemaParameters(schema), 7);
    assert.equal(countSchemaParameters(null), 0);
  });

  it("counts a repeated sub-schema twice and a self-reference once", () => {
    const schema = { type: "object", properties: { child: {} } };
    schema.properties.child = schema;
    assert.equal(countSchemaParameters(schema), 1);

    const shared = { type: "object", properties: { x: { type: "string" } } };
    assert.equal(countSchemaParameters({ type: "object", properties: { a: shared, b: shared } }), 4);
  });
});
