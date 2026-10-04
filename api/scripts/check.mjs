// Validates the contract: package version == info.version, every WebSocket
// example matches its schema, and every event has an example.
import { readFileSync, readdirSync, existsSync } from "node:fs";
import { join } from "node:path";
import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";
import { apiDir, bundle, loadEvents, readJson, readSpec } from "./lib.mjs";

const errors = [];
const spec = readSpec();
const pkg = readJson(join(apiDir, "package.json"));

if (pkg.version !== spec.info.version) {
  errors.push(`api/package.json version ${pkg.version} differs from openapi.yaml info.version ${spec.info.version}`);
}

const events = loadEvents();
const defs = bundle(spec, events);
const ajv = new Ajv2020({ strict: false, allErrors: true });
addFormats(ajv);

let validated = 0;
for (const dir of ["client", "server"]) {
  const examplesDir = join(apiDir, "websocket", "examples", dir);
  for (const name of Object.keys(events[dir])) {
    const file = join(examplesDir, `${name}.json`);
    if (!existsSync(file)) {
      errors.push(`missing example websocket/examples/${dir}/${name}.json`);
      continue;
    }
    const validate = ajv.compile({ $defs: defs, $ref: `#/$defs/${events[dir][name].title}` });
    if (!validate(JSON.parse(readFileSync(file, "utf8")))) {
      errors.push(`websocket/examples/${dir}/${name}.json: ${ajv.errorsText(validate.errors)}`);
    }
    validated++;
  }
  for (const f of existsSync(examplesDir) ? readdirSync(examplesDir) : []) {
    if (!(f.replace(".json", "") in events[dir])) errors.push(`example without schema: websocket/examples/${dir}/${f}`);
  }
}

// Negative checks: the schemas must actually reject bad payloads.
const negative = [
  ["ClientMessageEvent", { type: "message", chat_id: "42", data: { type: "text", content: "x" } }],
  ["ServerMessageEvent", { type: "message", event_id: 1, chat_id: "42", data: {} }],
  ["ClientMessageEvent", { type: "message", client_temp_id: "c-1", chat_id: "42", data: { type: "file", content: "x" } }],
  ["ClientMessageEvent", { type: "message", client_temp_id: "c-1", chat_id: "42", data: { type: "file", attachment_id: null } }],
  ["ServerHelloEvent", { type: "hello", event_id: "1", chat_id: "42", data: { api_version: "2.0.0", min_client_api_version: "2.0.0", user_id: "1" } }],
];
const sendMessage = (body) => ({ client_temp_id: "c-1", ...body });
negative.push(
  ["SendMessageRequest", sendMessage({ type: "text" })],
  ["SendMessageRequest", sendMessage({ type: "text", content: null })],
  ["SendMessageRequest", sendMessage({ type: "text", content: "" })],
  ["SendMessageRequest", sendMessage({ type: "text", content: "x", attachment_id: "5" })],
  ["SendMessageRequest", sendMessage({ type: "file" })],
  ["SendMessageRequest", sendMessage({ type: "file", attachment_id: null })],
);
// Compatible MINOR releases add fields to server events; clients must be able to ignore them.
const positive = [
  ["SendMessageRequest", sendMessage({ type: "text", content: "x" })],
  ["SendMessageRequest", sendMessage({ type: "file", attachment_id: "5" })],
  ["SendMessageRequest", sendMessage({ type: "voice", attachment_id: "5", content: null })],
  ["ServerHelloEvent", { type: "hello", event_id: "1", chat_id: null, new_envelope_field: 1, data: { api_version: "2.1.0", min_client_api_version: "2.0.0", user_id: "1", new_field: true } }],
];
for (const [title, payload] of positive) {
  const validateOk = ajv.compile({ $defs: defs, $ref: `#/$defs/${title}` });
  if (!validateOk(payload)) errors.push(`${title} rejected a valid payload: ${ajv.errorsText(validateOk.errors)}`);
}
for (const [title, payload] of negative) {
  if (ajv.compile({ $defs: defs, $ref: `#/$defs/${title}` })(payload)) errors.push(`${title} accepted an invalid payload`);
}

if (errors.length) {
  console.error(errors.map((e) => `✗ ${e}`).join("\n"));
  process.exit(1);
}
console.log(`✓ version ${spec.info.version}; ${validated} WebSocket examples valid`);
