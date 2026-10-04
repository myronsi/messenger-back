// Checks that info.version was bumped as far as the changes require.
//   node check-version-bump.mjs <base-openapi.yaml> <new-openapi.yaml>
// Breaking change -> MAJOR, any other API change -> MINOR, docs-only change -> PATCH.
// While the baseline is a pre-release (2.0.0-alpha.N) every change only needs a higher version.
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { parse } from "yaml";

const [baseFile, newFile] = process.argv.slice(2);
if (!baseFile || !newFile) {
  console.error("usage: check-version-bump.mjs <base.yaml> <new.yaml>");
  process.exit(2);
}
const oasdiff = process.env.OASDIFF || "oasdiff";

const SEMVER = /^(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$/;
function semver(v) {
  const m = SEMVER.exec(String(v));
  if (!m) throw new Error(`not a semantic version: ${v}`);
  return { major: +m[1], minor: +m[2], patch: +m[3], pre: m[4] ? m[4].split(".") : [] };
}

function compareIdentifier(a, b) {
  const an = /^\d+$/.test(a), bn = /^\d+$/.test(b);
  if (an && bn) return Math.sign(+a - +b);
  if (an) return -1;
  if (bn) return 1;
  return a < b ? -1 : a > b ? 1 : 0;
}
function compare(a, b) {
  for (const k of ["major", "minor", "patch"]) if (a[k] !== b[k]) return Math.sign(a[k] - b[k]);
  if (!a.pre.length || !b.pre.length) return Math.sign(b.pre.length - a.pre.length);
  for (let i = 0; i < Math.max(a.pre.length, b.pre.length); i++) {
    if (a.pre[i] === undefined) return -1;
    if (b.pre[i] === undefined) return 1;
    const c = compareIdentifier(a.pre[i], b.pre[i]);
    if (c) return c;
  }
  return 0;
}

const stable = (v) => JSON.stringify(v, (_, x) =>
  x && typeof x === "object" && !Array.isArray(x)
    ? Object.fromEntries(Object.entries(x).sort(([a], [b]) => (a < b ? -1 : 1)))
    : x);

const base = parse(readFileSync(baseFile, "utf8"));
const next = parse(readFileSync(newFile, "utf8"));
const baseVersion = semver(base.info.version);
const newVersion = semver(next.info.version);
const problems = [];

if (next.servers?.[0]?.url !== `/api/v${newVersion.major}`) {
  problems.push(`servers[0].url must be /api/v${newVersion.major} to match the MAJOR of info.version (found ${next.servers?.[0]?.url})`);
}

const out = execFileSync(oasdiff, ["changelog", baseFile, newFile, "--format", "json"], { encoding: "utf8", maxBuffer: 64 * 1024 * 1024 });
const changes = (out.trim() ? JSON.parse(out) : []).filter((c) => c.id !== "api-version-not-bumped");
const breaking = changes.filter((c) => c.level === 3);

const strip = (doc) => { const copy = structuredClone(doc); delete copy.info.version; return copy; };
const docsChanged = stable(strip(base)) !== stable(strip(next));

let required = "none";
if (breaking.length) required = "major";
else if (changes.length) required = "minor";
else if (docsChanged) required = "patch";

const cmp = compare(newVersion, baseVersion);
const baseIsPrerelease = baseVersion.pre.length > 0;
const bumped = {
  none: cmp >= 0,
  patch: cmp > 0,
  minor: baseIsPrerelease ? cmp > 0 : newVersion.major > baseVersion.major || (newVersion.major === baseVersion.major && newVersion.minor > baseVersion.minor),
  major: baseIsPrerelease ? cmp > 0 : newVersion.major > baseVersion.major,
}[required];

if (cmp < 0) problems.push(`version went backwards: ${base.info.version} -> ${next.info.version}`);
else if (!bumped) {
  problems.push(`${required.toUpperCase()} bump required (${base.info.version} -> ${next.info.version})`);
  for (const c of (breaking.length ? breaking : changes).slice(0, 20)) problems.push(`  - ${c.text}`);
  if (breaking.length) problems.push("Breaking changes need a new MAJOR with a new /api/vN, or the expand-contract process in docs/api-compatibility.md.");
}

if (problems.length) {
  console.error(problems.map((p) => (p.startsWith(" ") ? p : `✗ ${p}`)).join("\n"));
  process.exit(1);
}
console.log(`✓ ${base.info.version} -> ${next.info.version} (required: ${required}, ${changes.length} API changes${docsChanged ? ", docs changed" : ""})`);
