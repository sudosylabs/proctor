import {mkdir, readFile, rename, rm, writeFile} from 'node:fs/promises';
import {dirname, resolve} from 'node:path';
import {fileURLToPath} from 'node:url';

import {renderReference} from './asyncapi-reference.mjs';

const siteRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const source = resolve(siteRoot, '../../server/asyncapi.json');
const outputDirectory = resolve(siteRoot, '../api/reference/realtime-protocol');
const download = resolve(siteRoot, 'static/asyncapi/asyncapi.json');

const input = await readFile(source, 'utf8');
const files = renderReference(JSON.parse(input));

await rm(outputDirectory, {recursive: true, force: true});
await mkdir(outputDirectory, {recursive: true});
for (const [name, contents] of files) {
  await writeFile(resolve(outputDirectory, name), contents);
}

await mkdir(dirname(download), {recursive: true});
await writeFile(`${download}.tmp`, input, 'utf8');
await rename(`${download}.tmp`, download);

console.log(`Generated ${files.size - 2} realtime message pages from server/asyncapi.json`);
