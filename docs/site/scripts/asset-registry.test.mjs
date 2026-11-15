import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
import {mkdtemp, mkdir, writeFile} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {resolve} from 'node:path';
import test from 'node:test';

import {auditAssetRegistry} from './asset-registry.mjs';

const today = '2026-08-24';
const validDiagram = `blocks:
  - row:
      - card: Request
        text: Arrives at a node
      - card: Saved
        accent: true
`;

// The first 24 bytes are all the audit reads: signature, IHDR length, IHDR,
// then the width and height.
function png(width, height) {
  const buffer = Buffer.alloc(33);
  Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]).copy(buffer, 0);
  buffer.writeUInt32BE(13, 8);
  buffer.write('IHDR', 12, 'ascii');
  buffer.writeUInt32BE(width, 16);
  buffer.writeUInt32BE(height, 20);
  return buffer;
}

function sha256(data) {
  return createHash('sha256').update(data).digest('hex');
}

function review(hash, checks) {
  return {
    status: 'approved',
    source_sha256: hash,
    reviewed: today,
    method: 'Rendered browser fixture review.',
    checks,
    viewports: [
      {name: 'desktop', width: 1440, height: 1024},
      {name: 'mobile', width: 390, height: 844},
    ],
  };
}

const common = {
  owner: 'Documentation maintainers',
  license: {status: 'pending', expression: null, note: 'Awaiting the documentation license decision.'},
  provenance: 'Original test fixture.',
  last_reviewed: today,
  review_triggers: ['.agents/skills/runtime-operations/references/runtime.md'],
};

const allChecks = [
  'text_containment',
  'connector_continuity',
  'rendered_legibility',
  'narrow_viewport',
  'print_contrast',
];

async function fixture({
  page = '<GovernedFigure asset="test-diagram" />\n<GovernedFigure asset="test-screen" />',
  diagram = validDiagram,
  diagramEntry = {},
  screenshotEntry = {},
  privacyStatus = 'approved',
  checks = allChecks,
  extraFiles = {},
} = {}) {
  const root = await mkdtemp(resolve(tmpdir(), 'proctor-assets-'));
  for (const directory of [
    'docs/public/static/assets/screenshots',
    'docs/public/diagrams',
    'docs/public/operator',
    'docs/api',
    '.agents/skills/runtime-operations/references',
  ]) {
    await mkdir(resolve(root, directory), {recursive: true});
  }
  const screen = png(100, 50);
  await writeFile(resolve(root, 'docs/public/diagrams/test-diagram.yaml'), diagram);
  await writeFile(resolve(root, 'docs/public/static/assets/screenshots/test.png'), screen);
  await writeFile(resolve(root, 'docs/public/operator/index.mdx'), page);
  await writeFile(resolve(root, '.agents/skills/runtime-operations/references/runtime.md'), '# Runtime\n');
  for (const [path, contents] of Object.entries(extraFiles)) {
    await writeFile(resolve(root, path), contents);
  }
  const assets = [
    {
      id: 'test-diagram',
      kind: 'diagram',
      source: 'diagrams/test-diagram.yaml',
      ...common,
      privacy_review: {status: privacyStatus, basis: 'Synthetic labels only.'},
      alt: 'A request arrives and is saved.',
      caption: 'A deterministic fixture.',
      theme: 'site',
      visual_system: 'proctor-diagrams-v2',
      visual_review: review(sha256(diagram), checks),
      ...diagramEntry,
    },
    {
      id: 'test-screen',
      kind: 'screenshot',
      path: 'static/assets/screenshots/test.png',
      public_path: '/assets/screenshots/test.png',
      ...common,
      privacy_review: {status: 'approved', basis: 'Synthetic fixture data only.'},
      alt: 'A synthetic screen.',
      caption: 'A deterministic screen.',
      width: 100,
      height: 50,
      max_bytes: 2000,
      theme: 'light',
      visual_review: review(sha256(screen), allChecks),
      ...screenshotEntry,
    },
  ];
  await writeFile(
    resolve(root, 'docs/public/assets.json'),
    JSON.stringify({schema_version: 4, assets}),
  );
  return root;
}

async function audit(options) {
  return auditAssetRegistry({repoRoot: await fixture(options), today});
}

function reports(result, text) {
  return result.failures.some((failure) => failure.includes(text));
}

test('accepts a complete diagram and screenshot', async () => {
  const result = await audit();
  assert.deepEqual(result.failures, []);
  assert.deepEqual(result.counts, {assets: 2, files: 2, references: 2});
});

test('rejects unregistered diagram sources and asset files', async () => {
  const result = await audit({
    extraFiles: {
      'docs/public/diagrams/orphan.yaml': validDiagram,
      'docs/public/static/assets/screenshots/orphan.png': png(1, 1),
    },
  });
  assert(reports(result, 'unregistered asset file: docs/public/diagrams/orphan.yaml'));
  assert(reports(result, 'unregistered asset file: docs/public/static/assets/screenshots/orphan.png'));
});

test('rejects public static files outside the governed asset root', async () => {
  const result = await audit({extraFiles: {'docs/public/static/bypass.svg': '<svg/>'}});
  assert(reports(result, 'ungoverned public static file'));
});

test('rejects registered assets without an authored reference', async () => {
  const result = await audit({page: '<GovernedFigure asset="test-screen" />'});
  assert(reports(result, 'asset test-diagram: registered asset is not referenced'));
});

test('rejects a diagram the renderer cannot draw', async () => {
  const diagram = `blocks:
  - row:
      - card: One
      - card: Two
      - card: Three
      - card: Four
      - card: Five
`;
  const result = await audit({diagram});
  assert(reports(result, 'no row holds more than 4'));
});

test('rejects a diagram source named after another id', async () => {
  const result = await audit({diagramEntry: {source: 'diagrams/other.yaml'}});
  assert(reports(result, 'source must be diagrams/test-diagram.yaml'));
});

test('requires the current diagram system and the site theme', async () => {
  const result = await audit({diagramEntry: {visual_system: 'proctor-assurance-v1', theme: 'light'}});
  assert(reports(result, 'visual_system must be proctor-diagrams-v2'));
  assert(reports(result, 'theme must be site'));
});

test('rejects screenshot fields on a diagram', async () => {
  const result = await audit({diagramEntry: {width: 1200}});
  assert(reports(result, 'width does not apply to a diagram'));
});

test('rejects screenshot dimension drift', async () => {
  const result = await audit({screenshotEntry: {width: 99}});
  assert(reports(result, 'dimensions do not match'));
});

test('rejects source bytes that drift from the approved visual review', async () => {
  const result = await audit({
    diagramEntry: {visual_review: review('0'.repeat(64), allChecks)},
  });
  assert(reports(result, 'does not match its approved visual review'));
});

test('requires the complete visual acceptance checklist', async () => {
  const result = await audit({checks: allChecks.filter((check) => check !== 'connector_continuity')});
  assert(reports(result, 'missing required check connector_continuity'));
});

test('requires an approved privacy review', async () => {
  const result = await audit({privacyStatus: 'pending'});
  assert(reports(result, 'privacy review must be approved'));
});

test('rejects unknown governed IDs and direct asset paths', async () => {
  const result = await audit({
    page:
      '<GovernedFigure asset="test-diagram" />\n<GovernedFigure asset="test-screen" />\n<GovernedFigure asset="missing" />\n[raw](/assets/screenshots/test.png)',
  });
  assert(reports(result, 'unknown governed asset'));
  assert(reports(result, 'direct asset paths are forbidden'));
});
