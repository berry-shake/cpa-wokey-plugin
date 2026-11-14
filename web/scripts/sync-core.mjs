// Copies the shared management page sources of cpa-provider-core into
// src/core (gitignored) so Vite, tsc and Vitest build them with src/provider.
//
// The core module directory comes from the Go toolchain, so it follows go.mod
// (and GOWORK, for a local checkout):
//   go list -m -f '{{.Dir}}' github.com/berry-shake/cpa-provider-core
// CPA_PROVIDER_CORE_DIR=/path/to/cpa-provider-core overrides it.

import { execFileSync } from "node:child_process";
import { chmodSync, cpSync, existsSync, readdirSync, rmSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const MODULE = "github.com/berry-shake/cpa-provider-core";
const web = join(dirname(fileURLToPath(import.meta.url)), "..");
const root = join(web, "..");

function goModuleDir() {
  const list = () =>
    execFileSync("go", ["list", "-m", "-f", "{{.Dir}}", MODULE], { cwd: root, encoding: "utf8", stdio: ["ignore", "pipe", "inherit"] }).trim();
  let dir = list();
  if (!dir) {
    // Required but not downloaded yet: go list leaves Dir empty.
    execFileSync("go", ["mod", "download", MODULE], { cwd: root, stdio: ["ignore", "inherit", "inherit"] });
    dir = list();
  }
  return dir;
}

const moduleDir = process.env.CPA_PROVIDER_CORE_DIR || goModuleDir();
const from = join(moduleDir, "web", "src");
if (!moduleDir || !existsSync(from)) {
  console.error(`sync-core: no web/src in ${moduleDir || "(empty module directory)"}`);
  process.exit(1);
}
// The module cache is read-only and the copy keeps its modes: make it
// writable so the next sync can replace it.
function makeWritable(dir) {
  if (!existsSync(dir)) return;
  chmodSync(dir, 0o755);
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) makeWritable(path);
    else chmodSync(path, 0o644);
  }
}

const to = join(web, "src", "core");
makeWritable(to);
rmSync(to, { recursive: true, force: true });
// The core's own tests run in the core repository against its test provider.
cpSync(from, to, { recursive: true, filter: (path) => !/\.test\.tsx?$/.test(path) });
makeWritable(to);
console.log(`sync-core: ${from} -> ${relative(root, to)}`);
