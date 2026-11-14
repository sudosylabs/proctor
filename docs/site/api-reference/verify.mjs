// Checks that the generated reference covers the contract exactly: one page per
// operation, one page per area, one sidebar entry each, and the Proctor
// extensions carried through unchanged.

export function verifyReference({operations, tags}, files) {
  const failures = [];
  const ids = new Set();
  for (const operation of operations) {
    if (ids.has(operation.id)) failures.push(`${operation.operationId}: page id ${operation.id} is used twice`);
    ids.add(operation.id);
    if (!files.has(`${operation.id}.mdx`)) failures.push(`${operation.operationId}: endpoint page is missing`);
    if (!tags.some((tag) => tag.name === operation.tag)) failures.push(`${operation.operationId}: tag ${operation.tag} is not declared`);
    const codes = operation.errors.flatMap((group) => group.codes.map((entry) => entry.code));
    const declared = operation.extensions['x-proctor-error-codes'] ?? [];
    if (JSON.stringify([...codes].sort()) !== JSON.stringify([...declared].sort())) {
      failures.push(`${operation.operationId}: rendered error codes differ from x-proctor-error-codes`);
    }
    if (operation.samples.length !== 4) failures.push(`${operation.operationId}: request samples are missing`);
  }
  const sidebar = files.get('sidebar.ts') ?? '';
  for (const operation of operations) {
    const occurrences = sidebar.split(`"reference/${operation.id}"`).length - 1;
    if (occurrences !== 1) failures.push(`${operation.operationId}: sidebar has ${occurrences} entries`);
  }
  for (const tag of tags) {
    if (!files.has(`${tag.slug}.mdx`)) failures.push(`${tag.name}: area page is missing`);
    if (tag.operations.length === 0) failures.push(`${tag.name}: area has no endpoints`);
  }
  if (failures.length > 0) throw new Error(failures.join('\n'));
  return {operationCount: operations.length, tagCount: tags.length};
}
