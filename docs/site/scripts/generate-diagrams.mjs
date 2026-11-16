import {mkdir, writeFile} from 'node:fs/promises';
import {dirname, resolve} from 'node:path';
import {fileURLToPath} from 'node:url';

import {loadDiagrams} from '../diagrams/index.mjs';

const siteRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const output = resolve(siteRoot, 'src/generated/diagrams.json');

const {diagrams, failures} = await loadDiagrams();
if (failures.length > 0) {
  console.error(failures.join('\n'));
  process.exit(1);
}
await mkdir(dirname(output), {recursive: true});
await writeFile(output, `${JSON.stringify(diagrams, null, 1)}\n`);
console.log(`Generated ${Object.keys(diagrams).length} diagrams`);
