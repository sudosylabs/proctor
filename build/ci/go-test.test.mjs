// Copyright 2026 SudoSylabs
// SPDX-License-Identifier: AGPL-3.0-only

import assert from "node:assert/strict";
import { mkdtemp, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { failedTopLevelTests, rerunArgs, runGoTests, testReport } from "./go-test.mjs";

test("JUnit escapes names and retains failure/skip outcomes", () => {
  const report = testReport(new Map([
    ["one", { name: 'Test<&"', package: "example.test/pkg", elapsed: 1, action: "fail" }],
    ["two", { name: "TestSkipped", package: "example.test/pkg", elapsed: 0, action: "skip" }],
  ]), 1);
  assert.equal(report.tests, 2);
  assert.equal(report.failures, 1);
  assert.equal(report.skipped, 1);
  assert.match(report.junit, /Test&lt;&amp;&quot;/);
  assert.match(report.junit, /<skipped\/>/);
});

test("process failures without test events cannot produce a passing report", () => {
  assert.equal(testReport(new Map(), 2).failures, 1);
  assert.equal(testReport(new Map(), 0).failures, 0);
});

test("runner preserves exit status and writes distinct artifacts on repeated runs", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "proctor-ci-report-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const command = join(directory, "fake-go");
  await writeFile(command, `#!/bin/sh
printf '%s\\n' '{"Action":"pass","Package":"example.test/pkg","Test":"TestExample","Elapsed":0.1}'
printf 'fixture diagnostic\\n' >&2
exit 7
`, { mode: 0o755 });
  const env = { ...process.env, PROCTOR_TEST_REPORT_DIR: join(directory, "reports"), GITHUB_STEP_SUMMARY: join(directory, "summary.md") };
  for (let i = 0; i < 2; i++) {
    assert.equal(await runGoTests(["-race", "./..."], { command, cwd: directory, env }), 7);
  }
  const reports = await readdir(env.PROCTOR_TEST_REPORT_DIR);
  assert.equal(reports.length, 2);
  const report = join(env.PROCTOR_TEST_REPORT_DIR, reports[0]);
  assert.match(await readFile(join(report, "stderr.log"), "utf8"), /fixture diagnostic/);
  assert.match(await readFile(join(report, "junit.xml"), "utf8"), /failures="1"/);
  const invocation = JSON.parse(await readFile(join(report, "command.json"), "utf8"));
  assert.ok(invocation.args.includes("-race"));
  assert.ok(invocation.args.includes("-covermode=atomic"));
  assert.match(await readFile(env.GITHUB_STEP_SUMMARY, "utf8"), /exit: 7/);
});

test("a rerun keeps build flags and drops package patterns and test selection", () => {
  assert.deepEqual(
    rerunArgs(["-tags=integration", "-timeout=90m", "-p=1", ".", "./app", "-skip", "^(TestA)$", "-count=1", "-race", "-p", "2", "./...", "-run", "^TestB$"]),
    ["-tags=integration", "-timeout=90m", "-p=1", "-race", "-p", "2"],
  );
});

test("only failed top-level tests are rerun; build failures aren't", () => {
  const failed = failedTopLevelTests(new Map([
    ["a", { name: "TestFlaky", package: "example.test/pkg", action: "fail" }],
    ["b", { name: "TestFlaky/sub", package: "example.test/pkg", action: "fail" }],
    ["c", { name: "TestPassed", package: "example.test/pkg", action: "pass" }],
    ["d", { name: "package", package: "example.test/broken", action: "fail" }],
  ]));
  assert.deepEqual([...failed], [["example.test/pkg", ["TestFlaky"]]]);
});

test("a test that passes when rerun is labelled flaky and the run still fails", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "proctor-ci-flaky-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const command = join(directory, "fake-go");
  // The first invocation fails TestFlaky and TestBroken; the rerun, which
  // names them with -run, passes only TestFlaky.
  await writeFile(command, `#!/bin/sh
case "$*" in
  *-run*)
    printf '%s\\n' '{"Action":"pass","Package":"example.test/pkg","Test":"TestFlaky"}'
    printf '%s\\n' '{"Action":"fail","Package":"example.test/pkg","Test":"TestBroken"}'
    printf '%s\\n' '{"Action":"fail","Package":"example.test/pkg","Test":"TestMixed"}'
    exit 1 ;;
  *)
    printf '%s\\n' '{"Action":"fail","Package":"example.test/pkg","Test":"TestFlaky"}'
    printf '%s\\n' '{"Action":"fail","Package":"example.test/pkg","Test":"TestBroken"}'
    printf '%s\\n' '{"Action":"pass","Package":"example.test/pkg","Test":"TestFlaky"}'
    printf '%s\\n' '{"Action":"fail","Package":"example.test/pkg","Test":"TestMixed"}'
    printf '%s\\n' '{"Action":"pass","Package":"example.test/pkg","Test":"TestMixed"}'
    exit 1 ;;
esac
`, { mode: 0o755 });
  const env = { ...process.env, PROCTOR_TEST_REPORT_DIR: join(directory, "reports"), GITHUB_STEP_SUMMARY: join(directory, "summary.md"), GITHUB_SHA: "abc123" };
  assert.equal(await runGoTests(["./..."], { command, cwd: directory, env }), 1, "a flake never turns the run green");
  const [report] = await readdir(env.PROCTOR_TEST_REPORT_DIR);
  const flaky = JSON.parse(await readFile(join(env.PROCTOR_TEST_REPORT_DIR, report, "flaky.json"), "utf8"));
  assert.deepEqual(flaky, ["TestFlaky", "TestMixed"].map(name => ({ package: "example.test/pkg", name })));
  const junit = await readFile(join(env.PROCTOR_TEST_REPORT_DIR, report, "junit.xml"), "utf8");
  assert.match(junit, /<testcase name="TestMixed"[^>]*><failure /);
  const invocation = JSON.parse(await readFile(join(env.PROCTOR_TEST_REPORT_DIR, report, "command.json"), "utf8"));
  assert.equal(invocation.ci.sha, "abc123");
  assert.match(await readFile(env.GITHUB_STEP_SUMMARY, "utf8"), /Flaky[\s\S]*TestFlaky/);
});
