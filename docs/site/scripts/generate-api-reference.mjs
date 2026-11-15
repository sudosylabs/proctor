import {mkdir, readdir, rm, writeFile} from 'node:fs/promises';
import {dirname, resolve} from 'node:path';
import {fileURLToPath} from 'node:url';

import {loadReference, renderReferenceFiles} from '../api-reference/index.mjs';
import {verifyReference} from '../api-reference/verify.mjs';

const siteRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const outputDirectory = resolve(siteRoot, '../api/reference');

const reference = await loadReference();
const files = renderReferenceFiles(reference);
verifyReference(reference, files);

// The realtime protocol pages share this folder and have their own generator,
// so only top-level files and the types folder are replaced.
await mkdir(outputDirectory, {recursive: true});
for (const entry of await readdir(outputDirectory, {withFileTypes: true})) {
  if (entry.isFile()) await rm(resolve(outputDirectory, entry.name));
}
await rm(resolve(outputDirectory, 'types'), {recursive: true, force: true});
await mkdir(resolve(outputDirectory, 'types'), {recursive: true});
for (const [name, contents] of files) {
  await writeFile(resolve(outputDirectory, name), contents);
}

console.log(
  `Generated ${reference.operations.length} endpoint pages in ${reference.tags.length} areas and ${reference.types.length} shared type pages from server/openapi.json`,
);
