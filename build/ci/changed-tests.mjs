// Copyright 2026 SudoSylabs
// SPDX-License-Identifier: AGPL-3.0-only

// changed-tests finds the top-level Go test functions a change adds or edits
// and runs them many times, so a test that flakes from the day it is written
// fails its own pull request instead of someone else's later. Most flaky tests
// are flaky from their first commit.
//
//   node build/ci/changed-tests.mjs <base revision> [--integration] [--count=N]
//
// Without --integration it stresses tests in files without an integration
// build tag; with it, only tests in integration-tagged files.
//
// Most Store tests keep their body in server/store/storetest, a plain Go
// package that each store's _test.go files call through a one-line wrapper. A
// changed storetest entry point (an exported Test or Run function taking
// *testing.T) stresses the wrapper tests that call it and nothing else.

import { spawnSync } from "node:child_process";
import { existsSync, readdirSync, readFileSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import { pathToFileURL } from "node:url";

const storetestDirectory = "server/store/storetest/";
const isTestFile = (file) => file.endsWith("_test.go");
const isStoretestFile = (file) => file.startsWith(storetestDirectory) && file.endsWith(".go") && !isTestFile(file);
const testFunction = /^func (Test\w*)\(\w+ \*testing\.T\)/;
const storetestEntry = /^func ((?:Test|Run)\w*)\(\w+ \*testing\.T[,)]/;

// changedLines maps each changed _test.go or storetest file to the line
// numbers the change added or edited, from a zero-context diff.
export function changedLines(diff) {
  const files = new Map();
  let current;
  for (const line of diff.split("\n")) {
    if (line.startsWith("+++ ")) {
      current = line === "+++ /dev/null" ? undefined : line.slice(6);
      if (current && !isTestFile(current) && !isStoretestFile(current)) current = undefined;
      if (current && !files.has(current)) files.set(current, new Set());
      continue;
    }
    const hunk = /^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@/.exec(line);
    if (hunk && current) {
      const start = Number(hunk[1]);
      const length = hunk[2] === undefined ? 1 : Number(hunk[2]);
      // A pure deletion still edits the function around it.
      for (let number = start; number < start + Math.max(length, 1); number++) files.get(current).add(number);
    }
  }
  return files;
}

// spans lists the top-level functions matching pattern, each with the rows
// from its func line to the next top-level func.
function spans(source, pattern) {
  const rows = source.split("\n");
  const functions = [];
  rows.forEach((row, index) => {
    if (/^func /.test(row)) functions.push({ line: index + 1, name: pattern.exec(row)?.[1] });
  });
  return functions.flatMap((fn, index) => {
    if (!fn.name) return [];
    const end = index + 1 < functions.length ? functions[index + 1].line - 1 : rows.length;
    return [{ name: fn.name, start: fn.line, end, body: rows.slice(fn.line - 1, end).join("\n") }];
  });
}

// touchedTests returns the top-level test functions whose span holds a
// changed line. A span runs from a top-level func to the next one.
export function touchedTests(source, lines, pattern = testFunction) {
  const touched = new Set();
  for (const fn of spans(source, pattern)) {
    for (const line of lines) if (line >= fn.start && line <= fn.end) touched.add(fn.name);
  }
  return [...touched].sort();
}

// wrappers returns the test functions in source that call exactly one
// storetest entry point and that one is in entries. A test naming several,
// such as a conformance table, runs far more than the change touched.
export function wrappers(source, entries) {
  return spans(source, testFunction)
    .filter((fn) => {
      const called = new Set([...fn.body.matchAll(/\bstoretest\.((?:Test|Run)\w*)\b/g)].map((match) => match[1]));
      return called.size === 1 && entries.has([...called][0]);
    })
    .map((fn) => fn.name)
    .sort();
}

function goTestFiles(directory) {
  return readdirSync(directory, { recursive: true, withFileTypes: true })
    .filter((entry) => entry.isFile() && isTestFile(entry.name))
    .map((entry) => join(entry.parentPath, entry.name));
}

const isIntegration = (source) => /^\/\/go:build .*\bintegration\b/m.test(source);

// plan groups the touched tests by Go package directory, relative to the
// server module, keeping only the kind of file asked for.
export function plan(root, diff, integration) {
  const packages = new Map();
  const add = (path, tests) => {
    if (tests.length === 0) return;
    const directory = `./${relative(join(root, "server"), dirname(path))}`.replace(/\/$/, "");
    packages.set(directory, [...new Set([...(packages.get(directory) ?? []), ...tests])].sort());
  };
  const entries = new Set();
  for (const [file, lines] of changedLines(diff)) {
    const path = join(root, file);
    if (!existsSync(path) || !file.startsWith("server/")) continue;
    const source = readFileSync(path, "utf8");
    if (isStoretestFile(file)) {
      for (const entry of touchedTests(source, lines, storetestEntry)) entries.add(entry);
      continue;
    }
    if (isIntegration(source) !== integration) continue;
    add(path, touchedTests(source, lines));
  }
  if (entries.size > 0) {
    for (const path of goTestFiles(join(root, "server"))) {
      const source = readFileSync(path, "utf8");
      if (isIntegration(source) === integration) add(path, wrappers(source, entries));
    }
  }
  return packages;
}

function main(argv) {
  const base = argv.find((arg) => !arg.startsWith("--"));
  if (!base) {
    console.error("usage: node build/ci/changed-tests.mjs <base revision> [--integration] [--count=N]");
    return 2;
  }
  const integration = argv.includes("--integration");
  const count = Number(argv.find((arg) => arg.startsWith("--count="))?.slice(8) ?? (integration ? 5 : 20));
  const root = resolve(dirname(new URL(import.meta.url).pathname), "../..");
  const diff = spawnSync("git", ["diff", "-U0", "--no-color", `${base}...HEAD`, "--", "*_test.go", "server/store/storetest/*.go"], { cwd: root, encoding: "utf8" });
  if (diff.status !== 0) {
    console.error(diff.stderr);
    return 1;
  }
  const packages = plan(root, diff.stdout, integration);
  if (packages.size === 0) {
    console.log("No changed tests to stress.");
    return 0;
  }
  const runner = process.env.GO_TEST ? process.env.GO_TEST.split(" ") : ["go", "test"];
  let failed = 0;
  for (const [directory, tests] of packages) {
    const pattern = `^(${tests.join("|")})$`;
    console.log(`Stressing ${tests.length} test(s) in ${directory} ${count} times: ${tests.join(", ")}`);
    const args = [...runner.slice(1), "-race", `-count=${count}`, "-run", pattern];
    if (integration) args.push("-tags=integration");
    args.push(directory);
    const result = spawnSync(runner[0], args, { cwd: join(root, "server"), stdio: "inherit", env: process.env });
    if (result.status !== 0) failed++;
  }
  return failed === 0 ? 0 : 1;
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  process.exitCode = main(process.argv.slice(2));
}
