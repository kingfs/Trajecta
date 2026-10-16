import React from "react";
import { InlineTag } from "../common/Badges";
import {
  schemaAnnotations,
  schemaItems,
  schemaPropertyRows,
  schemaTypeLabel,
} from "../../lib/jsonSchema";

/*
 * A tool's parameters, drawn as the form the schema describes.
 *
 * The dialog used to print the raw JSON Schema, which answers "what is the
 * declaration" and not "what can I pass": `{"type":"object","properties":{...}}`
 * puts every parameter two levels down, and the one line a reader wants - the
 * name, its type, whether it is required - is spread across three of them. Here
 * each parameter is a row, nested objects and array elements are indented rows
 * under their parent, and the raw document stays behind the fold in the caller
 * for the keywords a form cannot say.
 */

const VALUE_TONES = new Map([
  ["string", "accent"],
  ["number", "gold"],
  ["integer", "gold"],
  ["boolean", "green"],
  ["object", "default"],
  ["array", "default"],
]);

function typeTone(label) {
  return VALUE_TONES.get(label) || "default";
}

export function ToolSchemaForm({ schema, t }) {
  const rows = schemaPropertyRows(schema);
  if (!schema || (!rows.length && schemaItems(schema) === null)) {
    return <p className="tool-schema-empty">{t("traceDetail.schemaEmpty")}</p>;
  }
  // The object's own keywords belong to the form, not to a parameter: a closed
  // object and an element type describe what the caller may send as a whole.
  const annotations = schemaAnnotations(schema);
  return (
    <div className="tool-schema">
      {rows.map((row) => (
        <SchemaRow key={row.name} name={row.name} schema={row.schema} required={row.required} t={t} />
      ))}
      {/* An array at the top of a schema - a tool that takes one list - has no
          property to hang its element on. */}
      {!rows.length ? <SchemaRow name={t("traceDetail.schemaItems")} schema={schemaItems(schema) || {}} required={false} t={t} /> : null}
      {annotations.additionalProperties === false ? <p className="tool-schema-constraint">{t("traceDetail.schemaClosed")}</p> : null}
    </div>
  );
}

function SchemaRow({ name, schema, required, t }) {
  const annotations = schemaAnnotations(schema);
  const type = schemaTypeLabel(schema);
  const nested = schemaPropertyRows(schema);
  const items = schemaItems(schema);
  const itemRows = items ? schemaPropertyRows(items) : [];
  const description = typeof schema.description === "string" ? schema.description : "";
  const title = typeof schema.title === "string" ? schema.title : "";
  const constraints = [];
  if (annotations.format) {
    constraints.push(t("traceDetail.schemaFormat", { value: annotations.format }));
  }
  if (annotations.minimum !== undefined || annotations.maximum !== undefined) {
    constraints.push(t("traceDetail.schemaRange", { min: annotations.minimum ?? "-", max: annotations.maximum ?? "-" }));
  }
  if (annotations.pattern) {
    constraints.push(t("traceDetail.schemaPattern", { value: annotations.pattern }));
  }
  if (annotations.hasDefault) {
    constraints.push(t("traceDetail.schemaDefault", { value: formatScalar(annotations.defaultValue) }));
  }

  return (
    <div className="tool-schema-row">
      <div className="tool-schema-head">
        <span className="tool-schema-name mono">{name}</span>
        <span className="trace-tag-group">
          {title ? <InlineTag>{title}</InlineTag> : null}
          <InlineTag tone={typeTone(type)}>{type}</InlineTag>
          <InlineTag tone={required ? "danger" : "default"}>{t(required ? "traceDetail.schemaRequired" : "traceDetail.schemaOptional")}</InlineTag>
        </span>
      </div>
      {description ? <p className="tool-schema-description">{description}</p> : null}
      {annotations.values.length ? (
        <p className="tool-schema-constraint">
          {t("traceDetail.schemaValues")}
          <span className="trace-tag-group">
            {annotations.values.slice(0, 24).map((value, index) => (
              <InlineTag key={`${formatScalar(value)}-${index}`}>{formatScalar(value)}</InlineTag>
            ))}
          </span>
        </p>
      ) : null}
      {constraints.length ? <p className="tool-schema-constraint">{constraints.join(" · ")}</p> : null}
      {/* An object parameter is a form of its own, indented under the row that
          declares it; the same goes for the object inside an array. */}
      {nested.length || itemRows.length ? (
        <div className="tool-schema-nested">
          {nested.map((row) => (
            <SchemaRow key={row.name} name={row.name} schema={row.schema} required={row.required} t={t} />
          ))}
          {itemRows.length ? (
            <>
              <p className="tool-schema-constraint">{t("traceDetail.schemaItems")}</p>
              {itemRows.map((row) => (
                <SchemaRow key={row.name} name={row.name} schema={row.schema} required={row.required} t={t} />
              ))}
            </>
          ) : null}
        </div>
      ) : null}
      {!nested.length && items && !itemRows.length ? (
        <p className="tool-schema-constraint">{t("traceDetail.schemaItemsOf", { type: schemaTypeLabel(items) })}</p>
      ) : null}
      {/* `additionalProperties: false` is a real constraint on the caller and a
          form that omitted it would be lying by omission. */}
      {annotations.additionalProperties === false ? <p className="tool-schema-constraint">{t("traceDetail.schemaClosed")}</p> : null}
    </div>
  );
}

function formatScalar(value) {
  if (value === null) {
    return "null";
  }
  // A string is quoted the way the raw schema quotes it: `default "."` says the
  // default is a string, where `default .` says nothing about it.
  if (typeof value === "string") {
    return JSON.stringify(value);
  }
  if (typeof value === "object") {
    try {
      return JSON.stringify(value);
    } catch {
      return String(value);
    }
  }
  return String(value);
}
