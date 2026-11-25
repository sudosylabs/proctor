// Copyright 2026 SudoSylabs
// SPDX-License-Identifier: AGPL-3.0-only

import assert from "node:assert/strict";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { changedLines, plan, touchedTests } from "./changed-tests.mjs";

const source = `package example

import "testing"

func helper() int { return 1 }

func TestUnchanged(t *testing.T) {
	_ = helper()
}

func TestEdited(t *testing.T) {
	t.Log("edited")
}

func BenchmarkIgnored(b *testing.B) {}
`;

test("a diff yields the changed lines of test files only", () => {
  const files = changedLines(`diff --git a/server/x_test.go b/server/x_test.go
--- a/server/x_test.go
+++ b/server/x_test.go
@@ -11,0 +12 @@ func TestEdited(t *testing.T) {
+	t.Log("edited")
diff --git a/server/x.go b/server/x.go
--- a/server/x.go
+++ b/server/x.go
@@ -1 +1 @@
-package x
+package y
`);
  assert.deepEqual([...files.keys()], ["server/x_test.go"]);
  assert.deepEqual([...files.get("server/x_test.go")], [12]);
});

test("only the test functions holding a changed line are touched", () => {
  assert.deepEqual(touchedTests(source, new Set([12])), ["TestEdited"]);
  assert.deepEqual(touchedTests(source, new Set([5])), [], "a helper change touches no test by itself");
  assert.deepEqual(touchedTests(source, new Set([15])), [], "benchmarks aren't stressed");
});

test("tests are grouped by package and split by the integration tag", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "proctor-changed-tests-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  await mkdir(join(root, "server", "pkg"), { recursive: true });
  await writeFile(join(root, "server", "pkg", "unit_test.go"), source);
  await writeFile(join(root, "server", "pkg", "db_test.go"), `//go:build integration\n\n${source}`);
  const diff = ["unit_test.go", "db_test.go"].map((name) => `+++ b/server/pkg/${name}\n@@ -1,0 +${name === "db_test.go" ? 14 : 12} @@\n+x`).join("\n");
  assert.deepEqual([...plan(root, diff, false)], [["./pkg", ["TestEdited"]]]);
  assert.deepEqual([...plan(root, diff, true)], [["./pkg", ["TestEdited"]]]);
});
