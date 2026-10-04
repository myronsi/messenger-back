// Generates dist/: the OpenAPI document, REST types, WebSocket event types and the version constant.
import { copyFileSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { compile } from "json-schema-to-typescript";
import openapiTS, { astToString } from "openapi-typescript";
import { pathToFileURL } from "node:url";
import { apiDir, bundle, loadEvents, readSpec } from "./lib.mjs";

const dist = join(apiDir, "dist");
rmSync(dist, { recursive: true, force: true });
mkdirSync(dist, { recursive: true });

const spec = readSpec();
const version = spec.info.version;
const banner = `/* Generated from api/openapi.yaml ${version}. Do not edit. */\n`;

copyFileSync(join(apiDir, "openapi.yaml"), join(dist, "openapi.yaml"));

const ast = await openapiTS(pathToFileURL(join(apiDir, "openapi.yaml")));
writeFileSync(join(dist, "schema.d.ts"), banner + astToString(ast));

const defs = bundle(spec, loadEvents());
const wsTypes = await compile(
  {
    title: "WebSocketEvent",
    description: "Any WebSocket event, in either direction.",
    oneOf: [{ $ref: "#/$defs/ClientEvent" }, { $ref: "#/$defs/ServerEvent" }],
    $defs: defs,
  },
  "WebSocketEvent",
  { bannerComment: banner, additionalProperties: false, declareExternallyReferenced: true, strictIndexSignatures: true },
);
writeFileSync(join(dist, "ws-events.d.ts"), wsTypes);

writeFileSync(join(dist, "index.js"), `export const API_VERSION = ${JSON.stringify(version)};\n`);
writeFileSync(
  join(dist, "index.d.ts"),
  `${banner}export declare const API_VERSION: ${JSON.stringify(version)};\n` +
    `export type { paths, components, operations } from "./schema.js";\n` +
    `export * from "./ws-events.js";\n`,
);
console.log(`✓ dist/ built for ${version}`);
