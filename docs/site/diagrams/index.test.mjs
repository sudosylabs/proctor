import assert from 'node:assert/strict';
import test from 'node:test';

import {loadDiagrams, parseDiagram} from './index.mjs';

function failures(source) {
  return parseDiagram(source, 'fixture').failures;
}

test('every authored diagram follows the grammar', async () => {
  const result = await loadDiagrams();
  assert.deepEqual(result.failures, []);
  assert(Object.keys(result.diagrams).length > 0);
});

test('normalizes a row with arrows, a branch, and connectors', () => {
  const {diagram, failures: found} = parseDiagram(`blocks:
  - row:
      - card: Create
      - card: Pending
        accent: true
        via: {label: Send, step: 1}
      - branch: true
        items:
          - title: Accepted
          - title: Expired
  - row:
      - card: Mail
    from:
      - {from: 2, to: 1, label: Then, dashed: true}
  - note: Done.
    rule: dashed
`);
  assert.deepEqual(found, []);
  const [first, second, note] = diagram.blocks;
  assert.equal(first.items[0].via, null);
  assert.deepEqual(first.items[1].via, {label: 'Send', step: 1, accent: false, dashed: false});
  assert.deepEqual(first.items[2].via, {});
  assert.equal(first.items[2].title, undefined);
  assert.deepEqual(second.from, [{label: 'Then', accent: false, dashed: true, from: 2, to: 1}]);
  assert.deepEqual(note, {type: 'note', text: 'Done.', rule: 'dashed'});
});

test('a row without links places its items side by side', () => {
  const {diagram} = parseDiagram(`blocks:
  - row:
      - card: One
      - card: Two
    links: none
`);
  assert.deepEqual(diagram.blocks[0].items.map((item) => item.via), [null, null]);
});

test('rejects rows wider than the column can read', () => {
  const rows = Array.from({length: 5}, (_, index) => `      - card: Item ${index}`).join('\n');
  assert(failures(`blocks:\n  - row:\n${rows}\n`).some((failure) => failure.includes('no row holds more than 4')));
  assert.deepEqual(failures(`blocks:\n  - row:\n${rows}\n    stack: true\n`), []);
});

test('rejects markup, links, and unknown fields', () => {
  const found = failures(`blocks:
  - row:
      - card: <b>Bold</b>
        text: See https://docs.example.edu
        colour: red
`);
  assert(found.some((failure) => failure.includes('markup is not allowed')));
  assert(found.some((failure) => failure.includes('must not contain a URL')));
  assert(found.some((failure) => failure.includes('unknown field colour')));
});

test('rejects connectors that point outside either row', () => {
  const found = failures(`blocks:
  - row:
      - card: One
  - row:
      - card: Two
    from:
      - {from: 2, to: 3}
`);
  assert(found.some((failure) => failure.includes('.from must name an item of the row above')));
  assert(found.some((failure) => failure.includes('.to must name an item of this row')));
});

test('rejects connectors with no row above', () => {
  const found = failures(`blocks:
  - note: First.
  - row:
      - card: Two
    from:
      - {from: 1, to: 1}
`);
  assert(found.some((failure) => failure.includes('needs a row above it')));
});

test('rejects an arrow into the first item of a row', () => {
  const found = failures(`blocks:
  - row:
      - card: One
        via: Start
`);
  assert(found.some((failure) => failure.includes('has no item to its left')));
});
