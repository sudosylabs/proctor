// Copyright 2026 SudoSylabs
// SPDX-License-Identifier: AGPL-3.0-only

// flake-report reads the Go test report directories go-test.mjs writes, as CI
// artifacts keep them, and prints which tests failed, how often, and which
// were flaky: labelled by a rerun, or failing and passing on the same commit.

import { readdir, readFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";

// reportDirectories finds every report under root. An artifact.json names the
// commit of the reports below it, for artifacts from before reports recorded
// their own.
async function reportDirectories(root, sha) {
  const found = [];
  const entries = await readdir(root, { withFileTypes: true });
  const artifact = entries.some((entry) => entry.name === "artifact.json") ? await readJSON(join(root, "artifact.json"), {}) : {};
  const inherited = artifact.sha ?? sha;
  if (entries.some((entry) => entry.isFile() && entry.name === "junit.xml")) found.push({ directory: root, sha: inherited });
  for (const entry of entries) {
    if (entry.isDirectory()) found.push(...await reportDirectories(join(root, entry.name), inherited));
  }
  return found;
}

const readJSON = async (path, fallback) => {
  try { return JSON.parse(await readFile(path, "utf8")); } catch { return fallback; }
};

// testCases reads go-test.mjs's own JUnit: one self-closing or failed
// testcase per line, top-level tests and subtests alike.
export function testCases(junit) {
  const cases = [];
  for (const match of junit.matchAll(/<testcase name="([^"]*)" classname="([^"]*)"[^>]*>(.*?)<\/testcase>/g)) {
    const unescape = (value) => value.replace(/&quot;/g, '"').replace(/&apos;/g, "'").replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&amp;/g, "&");
    cases.push({ name: unescape(match[1]), package: unescape(match[2]), failed: match[3].includes("<failure") });
  }
  return cases;
}

// summarize counts each top-level test over every report. A test is flaky
// when a rerun passed it, or when it failed and passed on the same commit in
// the same invocation: the race job and the plain job run a test differently,
// so a failure in one and a pass in the other proves nothing.
export function summarize(reports) {
  const tests = new Map();
  for (const report of reports) {
    const flaky = new Set(report.flaky.map((item) => `${item.package}.${item.name}`));
    for (const item of report.cases) {
      if (item.name === "package" || item.name.includes("/")) continue;
      const key = `${item.package}.${item.name}`;
      if (!tests.has(key)) tests.set(key, { key, runs: 0, failures: 0, labelled: 0, failedOn: new Set(), passedOn: new Set() });
      const entry = tests.get(key);
      entry.runs++;
      const where = report.sha ? `${report.sha} ${report.invocation ?? ""}` : undefined;
      if (item.failed) {
        entry.failures++;
        if (where) entry.failedOn.add(where);
      } else if (where) {
        entry.passedOn.add(where);
      }
      if (flaky.has(key)) entry.labelled++;
    }
  }
  return [...tests.values()]
    .filter((entry) => entry.failures > 0)
    .map((entry) => ({
      test: entry.key, runs: entry.runs, failures: entry.failures, labelled: entry.labelled,
      sameCommit: [...entry.failedOn].filter((sha) => entry.passedOn.has(sha)).length,
    }))
    .map((entry) => ({ ...entry, flaky: entry.labelled > 0 || entry.sameCommit > 0 }))
    .sort((a, b) => Number(b.flaky) - Number(a.flaky) || b.failures - a.failures || a.test.localeCompare(b.test));
}

export function markdown(rows, reportCount) {
  const flaky = rows.filter((row) => row.flaky);
  const lines = [`### Go test flakes`, "", `${reportCount} test reports; ${rows.length} tests failed at least once; ${flaky.length} flaky.`, ""];
  if (rows.length === 0) return `${lines.join("\n")}\n`;
  lines.push("| Test | Runs | Failures | Rerun passed | Failed and passed on one commit | Flaky |", "|---|---:|---:|---:|---:|---|");
  for (const row of rows) {
    lines.push(`| \`${row.test}\` | ${row.runs} | ${row.failures} | ${row.labelled} | ${row.sameCommit} | ${row.flaky ? "yes" : ""} |`);
  }
  return `${lines.join("\n")}\n`;
}

export async function flakeReport(root) {
  const reports = [];
  for (const { directory, sha } of await reportDirectories(resolve(root))) {
    const command = await readJSON(join(directory, "command.json"), {});
    // The invocation is the job and its go test flags; the coverage profile
    // path differs on every run.
    const args = (command.args ?? []).filter((arg) => !arg.startsWith("-coverprofile="));
    reports.push({
      sha: command.ci?.sha ?? sha,
      invocation: JSON.stringify([command.ci?.job ?? "", ...args]),
      cases: testCases(await readFile(join(directory, "junit.xml"), "utf8")),
      flaky: await readJSON(join(directory, "flaky.json"), []),
    });
  }
  return markdown(summarize(reports), reports.length);
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  if (!process.argv[2]) {
    console.error("usage: node build/ci/flake-report.mjs <directory of test reports>");
    process.exitCode = 2;
  } else {
    process.stdout.write(await flakeReport(process.argv[2]));
  }
}
