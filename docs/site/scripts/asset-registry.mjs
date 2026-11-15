import {createHash} from 'node:crypto';
import {lstat, readFile, readdir} from 'node:fs/promises';
import {dirname, extname, isAbsolute, relative, resolve, sep} from 'node:path';
import {fileURLToPath} from 'node:url';

import {parseDiagram} from '../diagrams/index.mjs';

const scriptRoot = dirname(fileURLToPath(import.meta.url));
const defaultRepoRoot = resolve(scriptRoot, '../../..');
// Every asset records ownership, provenance, privacy, accessible text, and an
// approved review of its exact source bytes.
const requiredEntryFields = [
  'id',
  'kind',
  'owner',
  'license',
  'provenance',
  'privacy_review',
  'alt',
  'caption',
  'theme',
  'last_reviewed',
  'review_triggers',
  'visual_review',
];
// A diagram is a YAML layout drawn by the site in its own type and colours.
const diagramFields = ['source', 'visual_system'];
export const DIAGRAM_SYSTEM = 'proctor-diagrams-v2';
export const DIAGRAM_THEME = 'site';
// A screenshot is a PNG served from the governed static root.
const screenshotFields = ['path', 'public_path', 'width', 'height', 'max_bytes'];
const allowedKinds = new Set(['diagram', 'screenshot']);
const maximumScreenshotBytes = 2_000_000;
const documentationExtensions = new Set(['.md', '.mdx']);
const requiredVisualChecks = new Set([
  'text_containment',
  'connector_continuity',
  'rendered_legibility',
  'narrow_viewport',
  'print_contrast',
]);
const requiredVisualViewports = new Map([
  ['desktop', {width: 1440, height: 1024}],
  ['mobile', {width: 390, height: 844}],
]);

function isInside(parent, child) {
  const path = relative(parent, child);
  return (
    path === '' ||
    (!path.startsWith(`..${sep}`) && path !== '..' && !isAbsolute(path))
  );
}

async function walk(directory, {extensions, skip = new Set()} = {}) {
  let entries;
  try {
    entries = await readdir(directory, {withFileTypes: true});
  } catch (error) {
    if (error.code === 'ENOENT') {
      return [];
    }
    throw error;
  }

  const files = [];
  for (const entry of entries.sort((left, right) =>
    left.name.localeCompare(right.name),
  )) {
    const path = resolve(directory, entry.name);
    if (skip.has(path)) {
      continue;
    }
    if (entry.isDirectory()) {
      files.push(...(await walk(path, {extensions, skip})));
    } else if (!extensions || extensions.has(extname(entry.name).toLowerCase())) {
      files.push(path);
    }
  }
  return files;
}

function parsePNGDimensions(buffer, name, failures) {
  const signature = Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]);
  if (buffer.length < 24 || !buffer.subarray(0, 8).equals(signature)) {
    failures.push(`${name}: invalid PNG signature or truncated IHDR`);
    return null;
  }
  if (buffer.toString('ascii', 12, 16) !== 'IHDR') {
    failures.push(`${name}: PNG does not begin with an IHDR chunk`);
    return null;
  }
  return {width: buffer.readUInt32BE(16), height: buffer.readUInt32BE(20)};
}

function nonemptyString(value) {
  return typeof value === 'string' && value.trim().length > 0;
}

function isCalendarDate(value) {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value ?? '')) {
    return false;
  }
  const [year, month, day] = value.split('-').map(Number);
  const date = new Date(Date.UTC(year, month - 1, day));
  return (
    date.getUTCFullYear() === year &&
    date.getUTCMonth() === month - 1 &&
    date.getUTCDate() === day
  );
}

function validateVisualReview(review, label, failures, today) {
  if (!review || typeof review !== 'object' || Array.isArray(review)) {
    failures.push(`${label}: visual_review must be an object`);
    return;
  }
  if (review.status !== 'approved') {
    failures.push(`${label}: visual review status must be approved`);
  }
  if (!/^[a-f0-9]{64}$/.test(review.source_sha256 ?? '')) {
    failures.push(`${label}: visual review source_sha256 must be a lowercase SHA-256`);
  }
  if (!isCalendarDate(review.reviewed)) {
    failures.push(`${label}: visual review date must be a real date using YYYY-MM-DD`);
  } else if (review.reviewed > today) {
    failures.push(`${label}: visual review date cannot be in the future`);
  }
  if (!nonemptyString(review.method)) {
    failures.push(`${label}: visual review method must be a nonempty string`);
  }
  if (!Array.isArray(review.checks)) {
    failures.push(`${label}: visual review checks must be an array`);
  } else {
    const checks = new Set(review.checks);
    for (const check of requiredVisualChecks) {
      if (!checks.has(check)) {
        failures.push(`${label}: visual review is missing required check ${check}`);
      }
    }
    if (review.checks.some((check) => !nonemptyString(check))) {
      failures.push(`${label}: visual review checks must be nonempty strings`);
    }
    if (checks.size !== review.checks.length) {
      failures.push(`${label}: visual review checks must not contain duplicates`);
    }
  }

  if (!Array.isArray(review.viewports)) {
    failures.push(`${label}: visual review viewports must be an array`);
    return;
  }
  const viewportsByName = new Map();
  for (const viewport of review.viewports) {
    if (
      !viewport ||
      typeof viewport !== 'object' ||
      Array.isArray(viewport) ||
      !nonemptyString(viewport.name) ||
      !Number.isSafeInteger(viewport.width) ||
      viewport.width <= 0 ||
      !Number.isSafeInteger(viewport.height) ||
      viewport.height <= 0
    ) {
      failures.push(
        `${label}: each visual review viewport requires a name and positive integer dimensions`,
      );
      continue;
    }
    if (viewportsByName.has(viewport.name)) {
      failures.push(`${label}: visual review viewport ${viewport.name} is duplicated`);
    }
    viewportsByName.set(viewport.name, viewport);
  }
  for (const [name, expected] of requiredVisualViewports) {
    const actual = viewportsByName.get(name);
    if (
      !actual ||
      actual.width !== expected.width ||
      actual.height !== expected.height
    ) {
      failures.push(
        `${label}: visual review requires ${name} viewport ${expected.width}x${expected.height}`,
      );
    }
  }
}

function validateEntryMetadata(entry, index, failures, today) {
  const label = nonemptyString(entry?.id)
    ? `asset ${entry.id}`
    : `asset at index ${index}`;
  if (!entry || typeof entry !== 'object' || Array.isArray(entry)) {
    failures.push(`${label}: registry entry must be an object`);
    return;
  }
  for (const field of requiredEntryFields) {
    if (!(field in entry)) {
      failures.push(`${label}: ${field} is required`);
    }
  }
  if (!/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(entry.id ?? '')) {
    failures.push(`${label}: id must be lowercase kebab-case`);
  }
  if (!allowedKinds.has(entry.kind)) {
    failures.push(`${label}: kind must be diagram or screenshot`);
  }
  const kindFields = entry.kind === 'diagram' ? diagramFields : screenshotFields;
  const otherFields = entry.kind === 'diagram' ? screenshotFields : diagramFields;
  for (const field of kindFields) {
    if (!(field in entry)) {
      failures.push(`${label}: ${field} is required for a ${entry.kind}`);
    }
  }
  for (const field of otherFields) {
    if (field in entry) {
      failures.push(`${label}: ${field} does not apply to a ${entry.kind}`);
    }
  }
  for (const field of ['owner', 'provenance', 'alt', 'caption', 'theme']) {
    if (!nonemptyString(entry[field])) {
      failures.push(`${label}: ${field} must be a nonempty string`);
    }
  }
  if (entry.kind === 'diagram') {
    if (entry.visual_system !== DIAGRAM_SYSTEM) {
      failures.push(`${label}: visual_system must be ${DIAGRAM_SYSTEM}`);
    }
    if (entry.theme !== DIAGRAM_THEME) {
      failures.push(`${label}: a diagram follows the site theme, so theme must be ${DIAGRAM_THEME}`);
    }
  } else {
    for (const field of ['width', 'height', 'max_bytes']) {
      if (!Number.isSafeInteger(entry[field]) || entry[field] <= 0) {
        failures.push(`${label}: ${field} must be a positive integer`);
      }
    }
  }

  if (!entry.license || !['pending', 'resolved'].includes(entry.license.status)) {
    failures.push(`${label}: license status must be pending or resolved`);
  } else if (
    entry.license.status === 'resolved' &&
    !nonemptyString(entry.license.expression)
  ) {
    failures.push(`${label}: resolved license requires an expression`);
  } else if (entry.license.status === 'pending' && !nonemptyString(entry.license.note)) {
    failures.push(`${label}: pending license requires an explanatory note`);
  }

  if (
    entry.privacy_review?.status !== 'approved' ||
    !nonemptyString(entry.privacy_review?.basis)
  ) {
    failures.push(`${label}: privacy review must be approved with a nonempty basis`);
  }

  if (!isCalendarDate(entry.last_reviewed)) {
    failures.push(`${label}: last_reviewed must be a real date using YYYY-MM-DD`);
  } else if (entry.last_reviewed > today) {
    failures.push(`${label}: last_reviewed cannot be in the future`);
  }
  if (
    !Array.isArray(entry.review_triggers) ||
    entry.review_triggers.length === 0 ||
    entry.review_triggers.some((trigger) => !nonemptyString(trigger))
  ) {
    failures.push(
      `${label}: review_triggers must contain at least one repository-relative path`,
    );
  }
  validateVisualReview(entry.visual_review, label, failures, today);
}

async function validateReviewTriggers(entry, label, repoRoot, failures) {
  for (const trigger of Array.isArray(entry?.review_triggers) ? entry.review_triggers : []) {
    if (isAbsolute(trigger) || trigger.includes('\\')) {
      failures.push(`${label}: review trigger ${trigger} must be repository-relative`);
      continue;
    }
    const triggerPath = resolve(repoRoot, trigger);
    if (!isInside(repoRoot, triggerPath)) {
      failures.push(`${label}: review trigger ${trigger} escapes the repository`);
      continue;
    }
    try {
      const triggerInfo = await lstat(triggerPath);
      if (!triggerInfo.isFile() || triggerInfo.isSymbolicLink()) {
        failures.push(`${label}: review trigger ${trigger} must be a regular file`);
      }
    } catch {
      failures.push(`${label}: review trigger ${trigger} does not exist`);
    }
  }
}

export async function auditAssetRegistry({
  repoRoot = defaultRepoRoot,
  today = new Date().toISOString().slice(0, 10),
} = {}) {
  const publicRoot = resolve(repoRoot, 'docs/public');
  const apiRoot = resolve(repoRoot, 'docs/api');
  const publicStaticRoot = resolve(publicRoot, 'static');
  const assetRoot = resolve(publicStaticRoot, 'assets');
  const diagramRoot = resolve(publicRoot, 'diagrams');
  const registryPath = resolve(publicRoot, 'assets.json');
  const generatedReferenceRoot = resolve(apiRoot, 'reference');
  const failures = [];

  let registry;
  try {
    registry = JSON.parse(await readFile(registryPath, 'utf8'));
  } catch (error) {
    return {
      failures: [`docs/public/assets.json: ${error.message}`],
      counts: {assets: 0, files: 0, references: 0},
    };
  }

  if (registry.schema_version !== 4) {
    failures.push('docs/public/assets.json: schema_version must be 4');
  }
  if (!Array.isArray(registry.assets)) {
    failures.push('docs/public/assets.json: assets must be an array');
    return {failures, counts: {assets: 0, files: 0, references: 0}};
  }

  const ids = new Set();
  const registeredPaths = new Map();
  for (const [index, entry] of registry.assets.entries()) {
    validateEntryMetadata(entry, index, failures, today);
    const label = nonemptyString(entry?.id)
      ? `asset ${entry.id}`
      : `asset at index ${index}`;
    if (ids.has(entry?.id)) {
      failures.push(`${label}: duplicate id`);
    }
    ids.add(entry?.id);
    await validateReviewTriggers(entry, label, repoRoot, failures);

    const isDiagram = entry?.kind === 'diagram';
    const relativePath = isDiagram ? entry.source : entry?.path;
    const field = isDiagram ? 'source' : 'path';
    const root = isDiagram ? diagramRoot : assetRoot;
    const prefix = isDiagram ? 'diagrams/' : 'static/assets/';
    if (
      !nonemptyString(relativePath) ||
      relativePath.includes('\\') ||
      isAbsolute(relativePath)
    ) {
      failures.push(`${label}: ${field} must be a portable docs/public-relative path`);
      continue;
    }
    const filePath = resolve(publicRoot, relativePath);
    if (!relativePath.startsWith(prefix) || !isInside(root, filePath)) {
      failures.push(`${label}: ${field} must remain under docs/public/${prefix.slice(0, -1)}`);
      continue;
    }
    if (isDiagram && relativePath !== `diagrams/${entry.id}.yaml`) {
      failures.push(`${label}: source must be diagrams/${entry.id}.yaml`);
    }
    if (!isDiagram) {
      if (extname(filePath).toLowerCase() !== '.png') {
        failures.push(`${label}: screenshots must use PNG`);
      }
      if (entry.max_bytes > maximumScreenshotBytes) {
        failures.push(`${label}: max_bytes exceeds the ${maximumScreenshotBytes}-byte PNG ceiling`);
      }
      const expectedPublicPath = `/${relativePath.slice('static/'.length)}`;
      if (entry.public_path !== expectedPublicPath) {
        failures.push(`${label}: public_path must be ${expectedPublicPath}`);
      }
    }
    if (registeredPaths.has(filePath)) {
      failures.push(`${label}: ${field} is already owned by ${registeredPaths.get(filePath)}`);
    }
    registeredPaths.set(filePath, entry.id);

    let fileInfo;
    let data;
    try {
      fileInfo = await lstat(filePath);
      data = await readFile(filePath);
    } catch (error) {
      failures.push(`${label}: registered file cannot be read (${error.message})`);
      continue;
    }
    if (!fileInfo.isFile() || fileInfo.isSymbolicLink()) {
      failures.push(`${label}: registered asset must be a regular file, not a symlink`);
      continue;
    }
    const sourceSHA256 = createHash('sha256').update(data).digest('hex');
    if (entry.visual_review?.source_sha256 !== sourceSHA256) {
      failures.push(
        `${label}: file SHA-256 ${sourceSHA256} does not match its approved visual review`,
      );
    }
    if (isDiagram) {
      failures.push(...parseDiagram(data.toString('utf8'), relative(repoRoot, filePath)).failures);
      continue;
    }
    if (data.byteLength > entry.max_bytes) {
      failures.push(`${label}: ${data.byteLength} bytes exceeds max_bytes ${entry.max_bytes}`);
    }
    const dimensions = parsePNGDimensions(data, label, failures);
    if (
      dimensions &&
      (dimensions.width !== entry.width || dimensions.height !== entry.height)
    ) {
      failures.push(
        `${label}: registered ${entry.width}x${entry.height} dimensions do not match file ${dimensions.width}x${dimensions.height}`,
      );
    }
  }

  const publicStaticFiles = await walk(publicStaticRoot);
  const assetFiles = publicStaticFiles.filter((file) => isInside(assetRoot, file));
  for (const file of publicStaticFiles) {
    if (!isInside(assetRoot, file)) {
      failures.push(`ungoverned public static file: ${relative(repoRoot, file)}`);
    }
  }
  const diagramFiles = await walk(diagramRoot);
  for (const file of [...assetFiles, ...diagramFiles]) {
    if (!registeredPaths.has(file)) {
      failures.push(`unregistered asset file: ${relative(repoRoot, file)}`);
    }
  }

  const references = new Map();
  const contentFiles = [
    ...(await walk(publicRoot, {extensions: documentationExtensions})),
    ...(await walk(apiRoot, {
      extensions: documentationExtensions,
      skip: new Set([generatedReferenceRoot]),
    })),
  ];
  let referenceCount = 0;
  for (const file of contentFiles) {
    const source = await readFile(file, 'utf8');
    const name = relative(repoRoot, file);
    for (const match of source.matchAll(
      /<GovernedFigure\b[^>]*\basset\s*=\s*["']([^"']+)["'][^>]*\/?\s*>/g,
    )) {
      referenceCount += 1;
      const id = match[1];
      references.set(id, (references.get(id) ?? 0) + 1);
      if (!ids.has(id)) {
        failures.push(`${name}: references unknown governed asset ${id}`);
      }
    }
    if (/\/assets\/[a-z0-9_./-]+\.(?:png|svg)/i.test(source)) {
      failures.push(
        `${name}: direct asset paths are forbidden; use GovernedFigure with a registry ID`,
      );
    }
  }
  for (const id of ids) {
    if (nonemptyString(id) && !references.has(id)) {
      failures.push(`asset ${id}: registered asset is not referenced by authored documentation`);
    }
  }

  return {
    failures,
    counts: {
      assets: registry.assets.length,
      files: assetFiles.length + diagramFiles.length,
      references: referenceCount,
    },
  };
}
