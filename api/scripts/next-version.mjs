// Prints the pre-release version for a merge to the main branch:
//   2.4.0 -> 2.4.0-next.<run>   2.0.0-alpha.1 -> 2.0.0-alpha.1.next.<run>
// The result is always lower than the version of the next stable/alpha release
// and higher than every earlier run of this workflow.
import { readSpec } from "./lib.mjs";

const run = process.argv[2] ?? process.env.GITHUB_RUN_NUMBER;
if (!/^\d+$/.test(String(run))) {
  console.error("usage: next-version.mjs <run number>");
  process.exit(2);
}
const version = readSpec().info.version;
console.log(version.includes("-") ? `${version}.next.${run}` : `${version}-next.${run}`);
