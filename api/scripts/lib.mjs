import { readFileSync, readdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { parse } from "yaml";

export const apiDir = join(dirname(fileURLToPath(import.meta.url)), "..");

export const readJson = (p) => JSON.parse(readFileSync(p, "utf8"));
export const readSpec = (p = join(apiDir, "openapi.yaml")) => parse(readFileSync(p, "utf8"));

const OPENAPI_REF = "../../openapi.yaml#/components/schemas/";

// The event schemas point into openapi.yaml so that entities are defined once.
// Bundling copies those entities into $defs, which every JSON Schema tool understands.
function rewriteRefs(node) {
  if (Array.isArray(node)) return node.map(rewriteRefs);
  if (node && typeof node === "object") {
    const out = {};
    for (const [k, v] of Object.entries(node)) {
      if (k === "$ref" && typeof v === "string") {
        if (v.startsWith(OPENAPI_REF)) out[k] = "#/$defs/" + v.slice(OPENAPI_REF.length);
        else if (v.startsWith("#/components/schemas/")) out[k] = "#/$defs/" + v.slice("#/components/schemas/".length);
        else if (v.endsWith(".schema.json")) out[k] = "#/$defs/" + eventKey(v);
        else out[k] = v;
      } else if (k !== "$schema") {
        out[k] = rewriteRefs(v);
      }
    }
    return out;
  }
  return node;
}

const eventKey = (file) => file.split("/").pop().replace(".schema.json", "");

export function loadEvents() {
  const events = { client: {}, server: {} };
  for (const dir of ["client", "server"]) {
    const d = join(apiDir, "websocket", dir);
    for (const f of readdirSync(d).filter((x) => x.endsWith(".schema.json")).sort()) {
      events[dir][eventKey(f)] = readJson(join(d, f));
    }
  }
  return events;
}

export function bundle(spec, events) {
  const defs = {};
  for (const [name, schema] of Object.entries(spec.components.schemas)) defs[name] = rewriteRefs(schema);
  const titles = { client: [], server: [] };
  for (const dir of ["client", "server"]) {
    for (const schema of Object.values(events[dir])) {
      defs[schema.title] = rewriteRefs(schema);
      titles[dir].push(schema.title);
    }
  }
  defs.ClientEvent = {
    title: "ClientEvent",
    description: "Every event a client may send.",
    oneOf: titles.client.map((t) => ({ $ref: `#/$defs/${t}` })),
  };
  defs.ServerEvent = {
    title: "ServerEvent",
    description: "Every event the server may send.",
    oneOf: titles.server.map((t) => ({ $ref: `#/$defs/${t}` })),
  };
  return defs;
}
