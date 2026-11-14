import assert from 'node:assert/strict';
import {readFile, readdir} from 'node:fs/promises';
import {dirname, resolve} from 'node:path';
import test from 'node:test';
import {fileURLToPath} from 'node:url';

const siteRoot = dirname(fileURLToPath(import.meta.url));
const repositoryRoot = resolve(siteRoot, '../..');
const referenceRoot = resolve(siteRoot, '../public/reference/configuration');

// Each sign-in plugin owns its provider's Settings and Secrets documents, which
// the deployment configuration holds opaque. The reference documents their
// fields beside the configuration's own, so a provider document's leaves are
// the union of the fields each plugin's type declares.
const pluginDocuments = new Map([
  ['ExternalAuthenticationProvider.Settings', [
    {directories: ['server/plugin/cas', 'server/platform/externalauth', 'server/config'], type: 'Settings'},
    {directories: ['server/plugin/oidc', 'server/platform/externalauth', 'server/config'], type: 'Settings'},
  ]],
  ['ExternalAuthenticationProvider.Secrets', [
    {directories: ['server/plugin/oidc'], type: 'Secrets'},
  ]],
]);

// goStructs reads the exported structs of the given packages, keyed by name.
// A later package does not replace a struct an earlier one declared.
async function goStructs(directories) {
  const structs = new Map();
  for (const directory of directories) {
    const root = resolve(repositoryRoot, directory);
    const files = (await readdir(root, {withFileTypes: true}))
      .filter((entry) => entry.isFile() && entry.name.endsWith('.go') && !entry.name.endsWith('_test.go'))
      .map((entry) => resolve(root, entry.name));
    const sources = await Promise.all(files.map((file) => readFile(file, 'utf8')));
    for (const source of sources) {
      for (const match of source.matchAll(/^type (\w+) struct \{\n([\s\S]*?)^\}/gm)) {
        if (structs.has(match[1])) continue;
        const fields = [...match[2].matchAll(
          /^\s*(\w+)\s+([^\s`]+)\s+`json:"([^",]+)[^"]*"`/gm,
        )].map((field) => ({
          jsonName: field[3],
          type: field[2],
        }));
        structs.set(match[1], fields);
      }
    }
  }
  return structs;
}

async function configurationStructs() {
  return goStructs(['server/config']);
}

async function leafPaths(structs, typeName, prefix = '') {
  const fields = structs.get(typeName);
  if (!fields?.length) return [prefix];

  const paths = await Promise.all(fields.map(async (field) => {
    const isSlice = field.type.startsWith('[]');
    // A type from another package is named by its package: externalauth.ClaimMapping.
    const childType = field.type.replace(/^\[\]/, '').replace(/^\*/, '').replace(/^\w+\./, '');
    const path = `${prefix}${prefix ? '.' : ''}${field.jsonName}${isSlice ? '[]' : ''}`;
    const documents = pluginDocuments.get(`${typeName}.${field.jsonName}`);
    if (documents) {
      const owned = await Promise.all(documents.map(async (document) =>
        leafPaths(await goStructs(document.directories), document.type, path)));
      return [path, ...new Set(owned.flat())];
    }
    return structs.get(childType)?.length
      ? leafPaths(structs, childType, path)
      : [path];
  }));
  return paths.flat();
}

async function documentedPaths() {
  const paths = [];
  for (const entry of await readdir(referenceRoot, {withFileTypes: true})) {
    if (!entry.isFile() || !entry.name.endsWith('.mdx')) continue;
    const source = await readFile(resolve(referenceRoot, entry.name), 'utf8');
    for (const match of source.matchAll(/^\| `([^`]+)`(?:<br \/>)?/gm)) {
      paths.push({path: match[1], file: entry.name});
    }
  }
  return paths;
}

test('the human configuration reference covers every JSON leaf exactly once', async () => {
  const structs = await configurationStructs();
  const expected = [...new Set(await leafPaths(structs, 'Config'))].sort();
  const documented = await documentedPaths();
  const counts = new Map();
  for (const item of documented) {
    counts.set(item.path, [...(counts.get(item.path) ?? []), item.file]);
  }

  const duplicates = [...counts]
    .filter(([, files]) => files.length > 1)
    .map(([path, files]) => `${path}: ${files.join(', ')}`);
  assert.deepEqual(duplicates, [], `duplicate configuration paths:\n${duplicates.join('\n')}`);
  assert.deepEqual(
    [...counts.keys()].sort(),
    expected,
    'update the human-written configuration reference when the Go configuration schema changes',
  );
});
