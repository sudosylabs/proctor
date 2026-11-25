// Copyright 2026 SudoSylabs
// SPDX-License-Identifier: AGPL-3.0-only

import assert from "node:assert/strict";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { testReport } from "./go-test.mjs";
import { flakeReport, summarize, testCases } from "./flake-report.mjs";

const junit = (outcomes) => testReport(new Map(outcomes.map(([name, action], index) => [
  String(index), { name, package: "example.test/pkg", action, elapsed: 0 },
])), 0).junit;

test("the report reads go-test.mjs's own JUnit", () => {
  assert.deepEqual(testCases(junit([["Test<A>", "fail"], ["TestB", "pass"]])), [
    { name: "Test<A>", package: "example.test/pkg", failed: true },
    { name: "TestB", package: "example.test/pkg", failed: false },
  ]);
});

test("a failure and a pass on one commit, or a passing rerun, make a test flaky", () => {
  const rows = summarize([
    { sha: "one", cases: [{ name: "TestSameCommit", package: "p", failed: true }, { name: "TestBroken", package: "p", failed: true }], flaky: [] },
    { sha: "one", cases: [{ name: "TestSameCommit", package: "p", failed: false }, { name: "TestBroken", package: "p", failed: true }], flaky: [] },
    { sha: "two", cases: [{ name: "TestRerun", package: "p", failed: true }, { name: "TestRerun/sub", package: "p", failed: true }], flaky: [{ package: "p", name: "TestRerun" }] },
  ]);
  assert.deepEqual(rows.map((row) => [row.test, row.flaky]), [
    ["p.TestRerun", true], ["p.TestSameCommit", true], ["p.TestBroken", false],
  ]);
  assert.equal(rows.find((row) => row.test === "p.TestBroken").failures, 2);

  const acrossJobs = summarize([
    { sha: "one", invocation: "race", cases: [{ name: "TestRaceOnly", package: "p", failed: true }], flaky: [] },
    { sha: "one", invocation: "plain", cases: [{ name: "TestRaceOnly", package: "p", failed: false }], flaky: [] },
  ]);
  assert.equal(acrossJobs[0].flaky, false, "a failure under -race and a pass without it is not a flake");
});

test("the report walks nested artifact directories", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "proctor-flake-report-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  for (const [name, action] of [["run-1/server-a", "fail"], ["run-2/server-b", "pass"]]) {
    await mkdir(join(root, name), { recursive: true });
    await writeFile(join(root, name, "junit.xml"), junit([["TestFlip", action]]));
    await writeFile(join(root, name, "command.json"), JSON.stringify({ ci: { sha: "same" } }));
  }
  // An artifact downloaded with its commit beside it, from before reports
  // recorded their own.
  for (const [name, action] of [["old/server-a", "fail"], ["old/server-b", "pass"]]) {
    await mkdir(join(root, name), { recursive: true });
    await writeFile(join(root, name, "junit.xml"), testReport(new Map([["x", { name: "TestOld", package: "example.test/pkg", action, elapsed: 0 }]]), 0).junit);
  }
  await writeFile(join(root, "old", "artifact.json"), JSON.stringify({ sha: "older" }));
  const report = await flakeReport(root);
  assert.match(report, /4 test reports; 2 tests failed at least once; 2 flaky/);
  assert.match(report, /`example\.test\/pkg\.TestOld` \| 2 \| 1 \| 0 \| 1 \| yes/);
  assert.match(report, /`example\.test\/pkg\.TestFlip` \| 2 \| 1 \| 0 \| 1 \| yes/);
});
