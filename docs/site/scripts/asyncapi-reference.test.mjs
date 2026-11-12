import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import test from 'node:test';

import {memberTable, messageSlug, renderReference} from './asyncapi-reference.mjs';

const document = JSON.parse(
  await readFile(new URL('../../../server/asyncapi.json', import.meta.url), 'utf8'),
);
const messageKeys = Object.values(document.channels.websocket.messages).map((ref) =>
  ref.$ref.split('/').pop(),
);

test('every channel message has exactly one page and one sidebar entry', () => {
  const files = renderReference(document);
  const pages = [...files.keys()].filter((name) => name.endsWith('.mdx') && name !== 'index.mdx');
  assert.deepEqual(pages.sort(), messageKeys.map((key) => `${messageSlug(key)}.mdx`).sort());

  const sidebar = files.get('sidebar.ts');
  for (const key of messageKeys) {
    const id = `"reference/realtime-protocol/${messageSlug(key)}"`;
    assert.equal(sidebar.split(id).length - 1, 1, `${key} sidebar entries`);
  }
});

test('the overview links the handshake, every command, and every close code', () => {
  const index = renderReference(document).get('index.mdx');
  assert.match(index, /\(\/api\/reference\/connect-web-socket\)/);
  for (const entry of document.channels.websocket['x-proctor-close-codes']) {
    assert.match(index, new RegExp(`\\| ${entry.code} \\| \`${entry.name}\``));
  }
  for (const key of ['ping', 'subscribe', 'unsubscribe']) {
    assert.match(index, new RegExp(`\\(/api/reference/realtime-protocol/${key}\\)`));
  }
});

test('an event page states its wire name and its data members', () => {
  const page = renderReference(document).get('exam-attempt-submitted-for-candidate.mdx');
  assert.match(page, /The frame's `event` member is `exam_attempt_submitted`\./);
  assert.match(page, /Addressed to one User/);
  assert.match(page, /\| `submission_id` \| Opaque identifier \| Yes \|/);
  assert.doesNotMatch(page, /candidate_user_id/);
});

test('an event without data says so', () => {
  const page = renderReference(document).get('exam-created.mdx');
  assert.match(page, /This event has no data\./);
});

test('the generator refuses a document that is not the Proctor AsyncAPI contract', () => {
  assert.throws(() => renderReference({asyncapi: '2.6.0', channels: {}}), /not the Proctor AsyncAPI 3\.0\.0 document/);
});

test('two messages that would share a page fail', () => {
  const colliding = structuredClone(document);
  colliding.components.messages['exam-created'] = colliding.components.messages.exam_created;
  colliding.channels.websocket.messages['exam-created'] = {$ref: '#/components/messages/exam-created'};
  assert.throws(() => renderReference(colliding), /two messages render to exam-created\.mdx/);
});

test('a description holding a backslash or a pipe stays inside its table cell', () => {
  const table = memberTable(document, {
    type: 'object',
    properties: {path: {type: 'string', description: 'ends in \\ | then {x}'}},
  });
  const row = table.split('\n')[2];
  assert.equal(row, '| `path` | string | No | ends in \\\\ \\| then \\{x\\} |');
  assert.equal(row.split(/(?<!\\)\|/).length - 2, 4, 'the row keeps exactly four cells');
});
