import {mkdir, rm, writeFile} from 'node:fs/promises';
import {dirname, resolve} from 'node:path';
import {fileURLToPath} from 'node:url';

import {loadProblems, renderProblemFiles} from '../problems/index.mjs';

const siteRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const outputDirectory = resolve(siteRoot, '../problems');

const problems = await loadProblems();
const files = renderProblemFiles(problems);

// Keep the tracked .gitignore; everything else in the root is generated.
await rm(outputDirectory, {recursive: true, force: true});
await mkdir(outputDirectory, {recursive: true});
await writeFile(resolve(outputDirectory, '.gitignore'), '*\n!.gitignore\n');
for (const [name, contents] of files) {
  const path = resolve(outputDirectory, name);
  await mkdir(dirname(path), {recursive: true});
  await writeFile(path, contents);
}

const authored = problems.filter((problem) => problem.guidance).length;
console.log(
  `Generated ${problems.length} error-code pages from the error catalog (${authored} with written guidance)`,
);
