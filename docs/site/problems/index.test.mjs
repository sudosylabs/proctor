import assert from 'node:assert/strict';
import test from 'node:test';

import {loadProblems, mdxText, renderProblemFiles} from './index.mjs';

const problems = await loadProblems();
const byCode = new Map(problems.map((problem) => [problem.code, problem]));

test('every catalogued code gets exactly one page and one sidebar entry', () => {
  const files = renderProblemFiles(problems);
  for (const problem of problems) {
    assert(files.has(`${problem.area}/${problem.code}.mdx`), problem.code);
  }
  const sidebar = files.get('sidebar.ts');
  for (const problem of problems) {
    assert.equal(sidebar.split(`"${problem.area}/${problem.code}"`).length, 2, problem.code);
  }
});

test('statuses follow the catalog rule and its documented exceptions', () => {
  assert.equal(byCode.get('access_policy.revision_conflict').status, 409);
  assert.equal(byCode.get('access_policy.invalid').status, 400);
  assert.equal(byCode.get('authentication.rate_limited').status, 429);
  assert.equal(byCode.get('authentication.dpop.use_nonce').status, 401);
  assert.equal(byCode.get('request.too_large').status, 413);
  assert.equal(byCode.get('server.internal').status, 500);
});

test('example titles come from the server localization', () => {
  assert.equal(byCode.get('access_policy.revision_conflict').title, 'Conflict');
  assert.equal(byCode.get('authentication.rate_limited').title, 'Too many requests');
});

test('pages list the endpoints that declare a code', () => {
  const paths = byCode.get('access_policy.revision_conflict').endpoints.map((endpoint) => endpoint.path);
  assert(paths.includes('/api/v1/access-policy'));
});

test('written guidance replaces the general advice', () => {
  const page = renderProblemFiles(problems).get('access_policy/access_policy.revision_conflict.mdx');
  assert.match(page, /expected_revision/);
  assert.doesNotMatch(page, /Someone else changed the resource after you read it/);
});

test('MDX markup is escaped outside inline code only', () => {
  assert.equal(mdxText('Use {a} and `{b}`'), 'Use \\{a\\} and `{b}`');
  assert.equal(mdxText('a < b'), 'a \\< b');
});
