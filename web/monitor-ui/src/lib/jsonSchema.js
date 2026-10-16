/*
 * A tool's parameters are a JSON Schema, and a JSON Schema read as JSON is a
 * document about a document: to answer "what can I pass in" a reader has to open
 * `properties`, then the property, then `items`, all while holding the enclosing
 * braces in their head. These helpers read the schema once and hand the dialog a
 * list of parameters it can draw as the form the schema describes.
 *
 * They are deliberately tolerant. The same declaration arrives wrapped
 * (`{type: "function", function: {parameters}}`), flat, as an Anthropic
 * `input_schema`, or as the JSON text of any of those, and a schema a newer
 * provider invented still has to render as something other than a crash.
 */

function isPlainObject(value) {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function parseSchemaValue(parameters) {
  if (parameters === null || parameters === undefined || parameters === "") {
    return null;
  }
  if (typeof parameters === "string") {
    try {
      return JSON.parse(parameters);
    } catch {
      return null;
    }
  }
  return parameters;
}

/*
 * The schema itself, from any of the shapes a declaration arrives in. The
 * unwrapping is guarded by the keys that make a wrapper a wrapper, so a schema
 * that genuinely has a property called `parameters` is left alone.
 */
export function parseSchemaDocument(parameters) {
  let value = parseSchemaValue(parameters);
  if (!isPlainObject(value)) {
    return null;
  }
  if (isPlainObject(value.function)) {
    value = parseSchemaValue(value.function.parameters ?? value.function.input_schema ?? value.function.parameters_json ?? null);
    return isPlainObject(value) ? value : null;
  }
  if (!value.properties && !value.items && !value.type) {
    if (value.parameters !== undefined) {
      const inner = parseSchemaValue(value.parameters);
      return isPlainObject(inner) ? inner : null;
    }
    if (value.input_schema !== undefined) {
      const inner = parseSchemaValue(value.input_schema);
      return isPlainObject(inner) ? inner : null;
    }
  }
  return value;
}

const JSON_TYPES = new Set(["string", "number", "integer", "boolean", "object", "array", "null"]);

/*
 * The type a parameter accepts, as the schema states it: a single type, a union
 * (`["string", "null"]`), an enumeration with no type of its own, or the
 * composition keywords. `any` is the honest answer for a schema that declares
 * none of them.
 */
export function schemaTypeLabel(schema) {
  if (!isPlainObject(schema)) {
    return "any";
  }
  if (schema.$ref) {
    return String(schema.$ref).split("/").pop() || "ref";
  }
  const declared = Array.isArray(schema.type) ? schema.type : schema.type ? [schema.type] : [];
  const types = declared.filter((type) => typeof type === "string" && JSON_TYPES.has(type));
  if (types.length) {
    return types.join(" | ");
  }
  if (Array.isArray(schema.enum) && schema.enum.length) {
    const kinds = [...new Set(schema.enum.map((item) => (item === null ? "null" : typeof item)))];
    return kinds.length === 1 ? `${kinds[0]} (enum)` : "enum";
  }
  for (const keyword of ["oneOf", "anyOf", "allOf"]) {
    if (Array.isArray(schema[keyword]) && schema[keyword].length) {
      return `${keyword} of ${schema[keyword].length}`;
    }
  }
  if (isPlainObject(schema.properties) || schema.additionalProperties !== undefined) {
    return "object";
  }
  if (schema.items !== undefined) {
    return "array";
  }
  return "any";
}

/*
 * One entry per declared property, in the order the schema declares them - the
 * order the author wrote, which is the order the caller will read them in.
 * `required` comes from the parent, because that is where JSON Schema keeps it.
 */
export function schemaPropertyRows(schema) {
  if (!isPlainObject(schema) || !isPlainObject(schema.properties)) {
    return [];
  }
  const required = new Set(Array.isArray(schema.required) ? schema.required.map(String) : []);
  return Object.entries(schema.properties).map(([name, property]) => ({
    name,
    schema: isPlainObject(property) ? property : {},
    required: required.has(name),
  }));
}

/* The keywords a form has to show beside the type, in the order a reader asks
   for them: what it accepts, what it defaults to, how it is formatted. */
export function schemaAnnotations(schema) {
  if (!isPlainObject(schema)) {
    return { values: [], defaultValue: undefined, hasDefault: false, format: "", pattern: "", minimum: undefined, maximum: undefined, additionalProperties: undefined };
  }
  const values = Array.isArray(schema.enum) ? schema.enum : Array.isArray(schema.const) ? [schema.const] : [];
  return {
    values,
    defaultValue: schema.default,
    hasDefault: Object.hasOwn(schema, "default"),
    format: typeof schema.format === "string" ? schema.format : "",
    pattern: typeof schema.pattern === "string" ? schema.pattern : "",
    minimum: typeof schema.minimum === "number" ? schema.minimum : undefined,
    maximum: typeof schema.maximum === "number" ? schema.maximum : undefined,
    additionalProperties: schema.additionalProperties,
  };
}

/* The schema of an array's elements, whether it was written as `items` or as a
   positional tuple. A tuple is shown as its first element, which is the shape
   the reader has to match anyway. */
export function schemaItems(schema) {
  if (!isPlainObject(schema)) {
    return null;
  }
  if (Array.isArray(schema.items)) {
    return isPlainObject(schema.items[0]) ? schema.items[0] : null;
  }
  return isPlainObject(schema.items) ? schema.items : null;
}

/* How many parameters the schema declares, nested ones included: the count the
   dialog's own tag reports, and the number a reader compares against the call
   they are looking at. */
export function countSchemaParameters(schema, ancestors = new Set()) {
  if (!isPlainObject(schema) || ancestors.has(schema)) {
    return 0;
  }
  // The guard is the branch, not the whole walk: the same sub-schema used by two
  // parameters is two parameters, while a schema that contains itself is one.
  const branch = new Set(ancestors);
  branch.add(schema);
  let total = 0;
  for (const row of schemaPropertyRows(schema)) {
    total += 1 + countSchemaParameters(row.schema, branch);
  }
  total += countSchemaParameters(schemaItems(schema), branch);
  return total;
}
