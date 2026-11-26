// Copyright 2026 SudoSylabs
// SPDX-License-Identifier: AGPL-3.0-only

import { spawn } from "node:child_process";
import { createWriteStream } from "node:fs";
import { appendFile, mkdir, mkdtemp, readFile, writeFile } from "node:fs/promises";
import { basename, join, resolve } from "node:path";
import { createInterface } from "node:readline";
import { finished } from "node:stream/promises";
import { pathToFileURL } from "node:url";

const escapeXML = (value) => String(value).replace(/[<>&"']/g, (char) => ({
  "<": "&lt;", ">": "&gt;", "&": "&amp;", '"': "&quot;", "'": "&apos;",
})[char]);

// Keep outcomes, not unbounded test output, in memory. Raw JSON and stderr are
// streamed to separate files, including when a package fails to build.
export function testReport(events, exitCode) {
  const cases = [...events.values()];
  if (exitCode !== 0 && !cases.some((item) => item.action === "fail")) {
    cases.push({ name: "go test process", package: "runner", action: "fail", elapsed: 0 });
  }
  const count = (action) => cases.filter((item) => item.action === action).length;
  const failures = count("fail");
  const skipped = count("skip");
  const xml = cases.map((item) => {
    const status = item.action === "fail" ? '<failure message="See events.jsonl and stderr.log"/>'
      : item.action === "skip" ? "<skipped/>" : "";
    return `  <testcase name="${escapeXML(item.name)}" classname="${escapeXML(item.package)}" time="${item.elapsed}">${status}</testcase>`;
  }).join("\n");
  return {
    tests: cases.length, failures, skipped,
    junit: `<?xml version="1.0" encoding="UTF-8"?>\n<testsuite name="go test" tests="${cases.length}" failures="${failures}" skipped="${skipped}">\n${xml}\n</testsuite>\n`,
  };
}

// go test flags that take their value as the next argument. Anything else
// that doesn't start with "-" is a package pattern.
const valueFlags = new Set(["-run", "-skip", "-timeout", "-p", "-parallel", "-cpu", "-count",
  "-tags", "-shuffle", "-bench", "-benchtime", "-coverpkg", "-exec", "-covermode", "-coverprofile"]);

// rerunArgs keeps the build and run flags of an invocation and drops its
// package patterns and test selection, so one rerun can name its own.
export function rerunArgs(args) {
  const kept = [];
  for (let i = 0; i < args.length; i++) {
    const arg = args[i];
    if (!arg.startsWith("-")) continue;
    const name = arg.split("=", 1)[0];
    const takesNext = valueFlags.has(name) && !arg.includes("=");
    if (!["-run", "-skip", "-count", "-shuffle", "-covermode", "-coverprofile", "-coverpkg"].includes(name)) {
      kept.push(arg);
      if (takesNext && i + 1 < args.length) kept.push(args[i + 1]);
    }
    if (takesNext) i++;
  }
  return kept;
}

// failedTopLevelTests groups the failed top-level tests by package. A package
// that failed without a test (a build or TestMain failure) is left out: a
// rerun can't tell a flake from a real break there.
export function failedTopLevelTests(results) {
  const byPackage = new Map();
  for (const item of results.values()) {
    if (item.action !== "fail" || item.name === "package" || item.name.includes("/")) continue;
    if (!byPackage.has(item.package)) byPackage.set(item.package, []);
    byPackage.get(item.package).push(item.name);
  }
  return byPackage;
}

const escapeRegExp = (value) => value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

// rerunFailures runs each failed test once more. A test that passes on the
// rerun is flaky. The caller's exit code doesn't change: a rerun only labels a
// flake, it never turns a run green.
async function rerunFailures(args, failed, { env, cwd, command }) {
  const flaky = [];
  for (const [packageName, tests] of failed) {
    const pattern = `^(${tests.map(escapeRegExp).join("|")})$`;
    const outcomes = new Map();
    const child = spawn(command, ["test", "-json", "-count=1", ...rerunArgs(args), "-run", pattern, packageName],
      { cwd, env, stdio: ["ignore", "pipe", "ignore"] });
    const lines = createInterface({ input: child.stdout });
    lines.on("line", (line) => {
      let event;
      try { event = JSON.parse(line); } catch { return; }
      if (event.Test && !event.Test.includes("/") && ["pass", "fail"].includes(event.Action)) outcomes.set(event.Test, event.Action);
    });
    await new Promise((done) => { child.on("error", done); child.on("close", done); });
    for (const name of tests) if (outcomes.get(name) === "pass") flaky.push({ package: packageName, name });
  }
  return flaky;
}

export async function runGoTests(args, { env = process.env, cwd = process.cwd(), command = "go" } = {}) {
  if (!env.PROCTOR_TEST_REPORT_DIR) throw new Error("PROCTOR_TEST_REPORT_DIR is required");
  const root = resolve(env.PROCTOR_TEST_REPORT_DIR);
  await mkdir(root, { recursive: true });
  const directory = await mkdtemp(join(root, `${basename(cwd)}-`));
  const commandArgs = ["test", "-json", "-count=1", "-shuffle=on",
    "-covermode=atomic", `-coverprofile=${join(directory, "coverage.out")}`, ...args];
  // The commit and run let a flake report match reruns of the same code.
  const ci = { sha: env.GITHUB_SHA, run: env.GITHUB_RUN_ID, attempt: env.GITHUB_RUN_ATTEMPT, ref: env.GITHUB_REF_NAME, job: env.GITHUB_JOB };
  await writeFile(join(directory, "command.json"), JSON.stringify({ cwd, args: commandArgs, ci }, null, 2));

  const raw = createWriteStream(join(directory, "events.jsonl"));
  const errors = createWriteStream(join(directory, "stderr.log"));
  const child = spawn(command, commandArgs, { cwd, env, stdio: ["ignore", "pipe", "pipe"] });
  const results = new Map();
  let reportError;
  const onError = (error) => { reportError = error; child.kill("SIGTERM"); };
  raw.on("error", onError);
  errors.on("error", onError);
  child.stdout.pipe(raw);
  child.stderr.pipe(errors);
  child.stderr.on("data", (data) => process.stderr.write(data));
  const lines = createInterface({ input: child.stdout });
  lines.on("line", (line) => {
    let event;
    try { event = JSON.parse(line); } catch { return; }
    if (event.Output) process.stdout.write(event.Output);
    if (!["pass", "fail", "skip"].includes(event.Action)) return;
    // Successful package summaries aren't extra test cases. Failed packages
    // still need a result when TestMain, compilation, or setup failed.
    if (!event.Test && event.Action !== "fail") return;
    const packageName = event.Package ?? event.ImportPath ?? "unknown";
    const name = event.Test ?? "package";
    const key = JSON.stringify([packageName, name]);
    const previous = results.get(key);
    results.set(key, {
      name, package: packageName,
      action: previous?.action === "fail" ? "fail" : event.Action,
      passed: previous?.passed || event.Action === "pass",
      elapsed: (previous?.elapsed ?? 0) + (event.Elapsed ?? 0),
    });
  });
  const terminate = () => child.kill("SIGTERM");
  process.on("SIGTERM", terminate);
  process.on("SIGINT", terminate);
  const exitCode = await new Promise((done) => {
    child.on("error", (error) => { reportError = error; });
    child.on("close", (code) => done(code ?? 1));
  });
  process.off("SIGTERM", terminate);
  process.off("SIGINT", terminate);
  await Promise.all([finished(raw), finished(errors)]);
  const report = testReport(results, reportError ? 1 : exitCode);
  await writeFile(join(directory, "junit.xml"), report.junit);
  const failed = exitCode !== 0 && !reportError ? failedTopLevelTests(results) : new Map();
  const flaky = failed.size > 0 ? await rerunFailures(args, failed, { env, cwd, command }) : [];
  for (const item of results.values()) {
    if (item.action === "fail" && item.passed && !item.name.includes("/") &&
        !flaky.some(test => test.package === item.package && test.name === item.name)) {
      flaky.push({ package: item.package, name: item.name });
    }
  }
  await writeFile(join(directory, "flaky.json"), `${JSON.stringify(flaky, null, 2)}\n`);
  const coverage = spawn("go", ["tool", "cover", `-func=${join(directory, "coverage.out")}`], { cwd, env });
  const coverageFile = createWriteStream(join(directory, "coverage.txt"));
  coverage.stdout.pipe(coverageFile);
  coverage.stderr.resume();
  coverage.on("error", () => coverageFile.end());
  await new Promise((done) => coverage.on("close", done));
  await finished(coverageFile);
  const coverageText = await readFile(join(directory, "coverage.txt"), "utf8");
  const total = /^total:.*?(\d+(?:\.\d+)?%)$/m.exec(coverageText)?.[1] ?? "unavailable";
  const flakyLines = flaky.length === 0 ? "" : `\n**Flaky** (both failed and passed; the run still fails):\n\n${flaky.map((item) => `- \`${item.package}\` ${item.name}`).join("\n")}\n`;
  const summary = `### Go tests: ${basename(directory)}\n\nTests: ${report.tests}; failures: ${report.failures}; skipped: ${report.skipped}; exit: ${exitCode}; statement coverage: ${total}.\n${flakyLines}\nSee this invocation's JUnit, raw events, stderr, and coverage artifacts.\n`;
  await writeFile(join(directory, "summary.md"), summary);
  if (env.GITHUB_STEP_SUMMARY) await appendFile(env.GITHUB_STEP_SUMMARY, `${summary}\n`);
  if (reportError) throw reportError;
  return exitCode;
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  try { process.exitCode = await runGoTests(process.argv.slice(2)); }
  catch (error) { console.error(error.message); process.exitCode = 1; }
}
