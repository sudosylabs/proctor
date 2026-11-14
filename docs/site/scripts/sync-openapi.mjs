import {mkdir, readFile, rename, writeFile} from 'node:fs/promises';
import {dirname, resolve} from 'node:path';
import {fileURLToPath} from 'node:url';
import {narrowErrorCodes} from './openapi-reference-input.mjs';

const siteRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const source = resolve(siteRoot, '../../server/openapi.json');
const download = resolve(siteRoot, 'static/openapi/openapi.json');
const referenceInput = resolve(siteRoot, '.openapi/reference.json');

async function replace(destination, contents) {
  const temporary = `${destination}.tmp`;
  await mkdir(dirname(destination), {recursive: true});
  await writeFile(temporary, contents, 'utf8');
  await rename(temporary, destination);
}

const input = await readFile(source, 'utf8');
const document = JSON.parse(input);

if (
  typeof document !== 'object' ||
  document === null ||
  typeof document.openapi !== 'string' ||
  typeof document.paths !== 'object' ||
  document.paths === null
) {
  throw new Error('server/openapi.json is not a usable OpenAPI document');
}

await replace(download, input);
await replace(referenceInput, `${JSON.stringify(narrowErrorCodes(document))}\n`);

console.log(`Published OpenAPI ${document.openapi} from server/openapi.json`);
