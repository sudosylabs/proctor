import assert from 'node:assert/strict';
import test from 'node:test';

import {describeRules, humanizeOperation, loadReference, renderReferenceFiles} from './index.mjs';
import {verifyReference} from './verify.mjs';

const reference = await loadReference();
const files = renderReferenceFiles(reference);
const submit = reference.operations.find((operation) => operation.operationId === 'submitExamAttempt');

test('every operation and area gets exactly one page and sidebar entry', () => {
  assert.deepEqual(verifyReference(reference, files), {
    operationCount: reference.operations.length,
    tagCount: reference.tags.length,
  });
  assert(reference.operations.length > 300);
});

test('page ids match the existing endpoint routes', () => {
  assert.equal(submit.id, 'submit-exam-attempt');
  assert(files.has('submit-exam-attempt.mdx'));
  assert.equal(humanizeOperation('getMFAStatus'), 'Get MFA status');
  assert.equal(humanizeOperation('connectWebSocket'), 'Connect WebSocket');
});

test('constraints are written for people, with the exact pattern kept', () => {
  assert.deepEqual(describeRules({pattern: '^[ybndrfg8ejkmcpqxot1uwisza345h769]{26}$'}), ['26-character Proctor ID']);
  assert.deepEqual(describeRules({minimum: 1}), ['1 or more']);
  assert.deepEqual(describeRules({minLength: 1, maxLength: 128}), ['1 to 128 characters']);
  const key = submit.headers.find((header) => header.name === 'Idempotency-Key');
  assert.equal(key.pattern, '^[A-Za-z0-9._~-]+$');
});

test('request samples use the contract example and mark the body lines', () => {
  const curl = submit.samples.find((sample) => sample.id === 'curl');
  assert.match(curl.code, /"participation_id": "ybndrfg8ejkmcpqxot1uwisza4"/);
  assert.match(curl.code, /Idempotency-Key: \$IDEMPOTENCY_KEY/);
  assert.match(curl.highlight, /^\{\d+-\d+\}$/);
  assert.deepEqual(submit.samples.map((sample) => sample.id), ['curl', 'js', 'python', 'http']);
});

test('errors are grouped by status and keep every declared code', () => {
  const declared = submit.extensions['x-proctor-error-codes'];
  const rendered = submit.errors.flatMap((group) => group.codes.map((entry) => entry.code));
  assert.deepEqual([...rendered].sort(), [...declared].sort());
  assert.deepEqual(submit.errors.map((group) => group.status), [...submit.errors.map((group) => group.status)].sort((a, b) => a - b));
  assert(submit.errors.every((group) => group.codes.every((entry) => entry.meaning.length > 0)));
});

test('response examples come from the contract or the schema, never placeholders', () => {
  const success = submit.examples.find((example) => example.ok);
  assert.equal(success.status, 201);
  assert.match(success.code, /"receipt_code": "MBY3-KBMN-EB83-TFW1"/);
  assert.doesNotMatch(success.code, /"string"/);
});
