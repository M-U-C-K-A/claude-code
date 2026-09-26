// The loader and page scripts are injected as text; a syntax slip only shows up
// at runtime inside Claude. These tests parse them the exact way the loader
// runs them, so a broken build fails here instead of silently in the app.

import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import vm from "node:vm";
import { buildLoader } from "../src/build.mjs";

const SRC = path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "src");
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;

test("built loader is valid JS with no leftover placeholders", () => {
  const { code, tag } = buildLoader();
  assert.doesNotThrow(() => new vm.Script(code, { filename: "loader.js" }));
  assert.doesNotMatch(code, /__CB_/);
  assert.match(tag, /^\d+\.\d+\.\d+-[0-9a-f]{10}$/);
});

test("page script parses the way the loader wraps it (async, uses CB)", () => {
  const page = fs.readFileSync(path.join(SRC, "page.js"), "utf8");
  // Loader wraps it as: (async () => { const CB = {...}; <page> })()
  assert.doesNotThrow(() => new AsyncFunction("CB", page));
});

test("theme.css has balanced braces and defines the control vars", () => {
  const css = fs.readFileSync(path.join(SRC, "theme.css"), "utf8");
  const open = (css.match(/{/g) || []).length;
  const close = (css.match(/}/g) || []).length;
  assert.equal(open, close, "unbalanced braces in theme.css");
  for (const v of ["--cb-image", "--cb-dim", "--cb-glass", "--cb-blur"]) {
    assert.ok(css.includes(v), `theme.css should reference ${v}`);
  }
  assert.ok(css.includes("--ayu-accent"), "theme.css should define the Ayu palette");
});

test("loader tag changes when the page script changes", () => {
  const original = fs.readFileSync(path.join(SRC, "page.js"), "utf8");
  const first = buildLoader().tag;
  try {
    fs.writeFileSync(path.join(SRC, "page.js"), `${original}\n// tweak\n`);
    assert.notEqual(buildLoader().tag, first, "tag must track the page script");
  } finally {
    fs.writeFileSync(path.join(SRC, "page.js"), original);
  }
});
