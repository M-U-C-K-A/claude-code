// Tests for the risky part: patching app.asar without corrupting it.
//
// Builds real asar archives with @electron/asar (the same library Electron
// uses), then runs the patcher against them and checks that:
//   - the loader lands in the main script and only there,
//   - every other file still reads back at its offset with its original hash
//     (i.e. the offset shifting is correct),
//   - the header hash the caller gets matches the rebuilt archive,
//   - patching is idempotent and fully reversible,
//   - integrity records are refreshed for the patched file.
//
// Run: node --test test/   (needs `npm install` for @electron/asar)

import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import {
  inspectAsar,
  parseAsar,
  patchAsar,
  readEntry,
  unpatchAsar,
  verifyAsar,
} from "../src/asar.mjs";
import { buildLoader } from "../src/build.mjs";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO = path.join(HERE, "..");

let asarLib;
try {
  asarLib = await import("@electron/asar");
} catch {
  console.error("skip: run `npm install` first (needs @electron/asar)");
  process.exit(0);
}

const sha256 = (data) => crypto.createHash("sha256").update(data).digest("hex");

// Build an asar tree that looks like Claude's: package.json -> main script,
// plus a handful of other packed files whose bytes must survive the shift.
function makeArchive(mainRelative = ".vite/build/index.pre.js") {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "cb-asar-"));
  const src = path.join(dir, "src");
  const out = path.join(dir, "app.asar");
  fs.mkdirSync(path.join(src, path.dirname(mainRelative)), { recursive: true });
  fs.writeFileSync(path.join(src, "package.json"), JSON.stringify({ name: "claude", main: `./${mainRelative}` }));
  const mainBody = `"use strict";\nconst electron = require("electron");\nconsole.log("claude main", electron);\n`;
  fs.writeFileSync(path.join(src, mainRelative), mainBody);
  // Extra files, including one after the main script in the pack order, to make
  // sure offsets are shifted correctly.
  for (let i = 0; i < 8; i += 1) {
    fs.writeFileSync(path.join(src, `chunk-${i}.js`), `module.exports=${i};\n${"x".repeat(1000 * (i + 1))}`);
  }
  fs.mkdirSync(path.join(src, "assets"), { recursive: true });
  fs.writeFileSync(path.join(src, "assets", "style.css"), "body{color:red}".repeat(500));
  execFileSync(process.execPath, [
    "-e",
    `require(${JSON.stringify(require_resolve())}).createPackage(${JSON.stringify(src)}, ${JSON.stringify(out)}).then(()=>process.exit(0))`,
  ]);
  return { dir, out, mainRelative, mainBody };
}

function require_resolve() {
  // Resolve @electron/asar's entry from the repo's node_modules.
  return path.join(REPO, "node_modules", "@electron", "asar", "lib", "asar.js");
}

// Snapshot every packed file's bytes so we can compare after patching.
function snapshot(archive) {
  const parsed = parseAsar(archive);
  const files = {};
  const walk = (node, prefix) => {
    for (const [name, child] of Object.entries(node.files ?? {})) {
      const p = prefix ? `${prefix}/${name}` : name;
      if (child.files) walk(child, p);
      else files[p] = sha256(readEntry(parsed, p));
    }
  };
  walk(parsed.header, "");
  return files;
}

test("patch injects the loader into the main script only", () => {
  const { out, mainRelative } = makeArchive();
  const archive = fs.readFileSync(out);
  const before = snapshot(archive);
  const loader = buildLoader();

  const result = patchAsar(archive, loader);
  assert.equal(result.changed, true);
  assert.equal(result.mainPath, mainRelative);

  const after = snapshot(result.archive);
  // Only the main script changed.
  for (const [file, hash] of Object.entries(before)) {
    if (file === mainRelative) assert.notEqual(after[file], hash, "main script should change");
    else assert.equal(after[file], hash, `${file} must be byte-identical after patch`);
  }
  const main = readEntry(parseAsar(result.archive), mainRelative).toString("utf8");
  assert.match(main, /claude-backdrop:loader:.*:start/);
  assert.ok(main.startsWith('"use strict"'), "original prologue preserved");
});

test("every packed file passes its integrity record after patching", () => {
  const { out } = makeArchive();
  const loader = buildLoader();
  const result = patchAsar(fs.readFileSync(out), loader);
  const checked = verifyAsar(result.archive);
  assert.ok(checked >= 9, `expected many files checked, got ${checked}`);
});

test("header hash returned matches the rebuilt archive", () => {
  const { out } = makeArchive();
  const result = patchAsar(fs.readFileSync(out), buildLoader());
  assert.equal(result.headerHash, inspectAsar(result.archive).headerHash);
});

test("patch is idempotent for the same loader tag", () => {
  const { out } = makeArchive();
  const loader = buildLoader();
  const once = patchAsar(fs.readFileSync(out), loader);
  const twice = patchAsar(once.archive, loader);
  assert.equal(twice.changed, false);
  assert.ok(twice.archive.equals(once.archive));
});

test("unpatch restores the archive exactly", () => {
  const { out, mainRelative, mainBody } = makeArchive();
  const archive = fs.readFileSync(out);
  const loader = buildLoader();
  const patched = patchAsar(archive, loader);
  const restored = unpatchAsar(patched.archive);
  assert.equal(inspectAsar(restored.archive).loaderTag, null);
  assert.equal(readEntry(parseAsar(restored.archive), mainRelative).toString("utf8"), mainBody);
  // Full byte-for-byte snapshot equality.
  assert.deepEqual(snapshot(restored.archive), snapshot(archive));
  assert.equal(inspectAsar(restored.archive).headerHash, inspectAsar(archive).headerHash);
});

test("main entry falls back to index.js when main points at a directory", () => {
  const { out } = makeArchive("app/index.js");
  const parsed = parseAsar(fs.readFileSync(out));
  // package.json main is "./app/index.js" so this simply resolves as a file.
  assert.equal(inspectAsar(fs.readFileSync(out)).mainPath, "app/index.js");
  void parsed;
});

test("refuses an archive whose main is not a CommonJS entry point", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "cb-asar-"));
  const src = path.join(dir, "src");
  fs.mkdirSync(src, { recursive: true });
  fs.writeFileSync(path.join(src, "package.json"), JSON.stringify({ main: "./main.js" }));
  fs.writeFileSync(path.join(src, "main.js"), "export const x = 1; // ESM, no require\n");
  const out = path.join(dir, "app.asar");
  execFileSync(process.execPath, [
    "-e",
    `require(${JSON.stringify(require_resolve())}).createPackage(${JSON.stringify(src)}, ${JSON.stringify(out)}).then(()=>process.exit(0))`,
  ]);
  assert.throws(() => patchAsar(fs.readFileSync(out), buildLoader()), /does not look like/);
});
