import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import test from 'node:test';

import {
  auditSearchIndex,
  buildSearchIndex,
  renderSearchData,
  searchableText,
} from './index.mjs';

test('source-only MDX comments never enter reader search data', () => {
  assert.equal(
    searchableText('Visible before. {/* TODO(owner): internal decision. */} Visible after.'),
    'Visible before. Visible after.',
  );
});

test('the tracked search index is current', async () => {
  assert.deepEqual(await auditSearchIndex(), []);
});

test('search covers authored pages, product areas, and every operation', async () => {
  const entries = await buildSearchIndex();
  const openapi = JSON.parse(await readFile(new URL('../../../server/openapi.json', import.meta.url), 'utf8'));
  const methods = new Set(['get', 'put', 'post', 'delete', 'patch', 'head', 'options', 'trace']);
  const operations = Object.values(openapi.paths).flatMap((item) => Object.keys(item).filter((key) => methods.has(key)));
  assert.equal(entries.filter((entry) => entry.kind === 'endpoint').length, operations.length);
  assert.equal(entries.filter((entry) => entry.kind === 'product-area').length, openapi.tags.length);
  assert(entries.some((entry) => entry.href === '/operator/'));
  assert(entries.some((entry) => entry.href === '/glossary/'));
  assert(entries.some((entry) => entry.href === '/api/guides/authentication'));
  assert(entries.some((entry) => entry.href === '/api/recipes/retention-policy'));
  assert(entries.some((entry) => entry.href === '/api/recipes/exam-exports'));
  assert(
    entries.some(
      (entry) =>
        entry.id === 'operation:getLiveness' &&
        entry.href === '/api/reference/get-liveness' &&
        entry.method === 'GET',
    ),
  );
});

test('search covers the realtime overview and every realtime message', async () => {
  const entries = await buildSearchIndex();
  const asyncapi = JSON.parse(await readFile(new URL('../../../server/asyncapi.json', import.meta.url), 'utf8'));
  const realtime = entries.filter((entry) => entry.kind === 'realtime-message');
  assert.equal(realtime.length, Object.keys(asyncapi.channels.websocket.messages).length + 1);
  assert(realtime.some((entry) => entry.href === '/api/reference/realtime-protocol/'));
  assert(
    realtime.some(
      (entry) =>
        entry.id === 'realtime:exam_attempt_submitted_for_candidate' &&
        entry.href === '/api/reference/realtime-protocol/exam-attempt-submitted-for-candidate',
    ),
  );
});

test('the generated adapter records its human-owned inputs', async () => {
  assert.match(renderSearchData(await buildSearchIndex()), /public MDX, server\/openapi\.json and server\/asyncapi\.json/);
});
