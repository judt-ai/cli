#!/usr/bin/env node
// Runs the judt binary from the platform package npm installed beside this one
// (@judt/cli-<platform>-<arch>, an optional dependency).
"use strict";
const { spawnSync } = require("node:child_process");

const pkg = `@judt/cli-${process.platform}-${process.arch}`;
const exe = process.platform === "win32" ? "judt.exe" : "judt";
let bin;
try {
  bin = require.resolve(`${pkg}/bin/${exe}`);
} catch {
  console.error(
    `judt: there's no build for ${process.platform}-${process.arch}, or npm skipped optional dependencies.\n` +
      `Reinstall without --no-optional, or install with: curl -fsSL https://judt.ai/install | sh`,
  );
  process.exit(1);
}
const result = spawnSync(bin, process.argv.slice(2), { stdio: "inherit" });
if (result.error) {
  console.error(`judt: ${result.error.message}`);
  process.exit(1);
}
if (result.signal) process.kill(process.pid, result.signal);
process.exit(result.status ?? 1);
