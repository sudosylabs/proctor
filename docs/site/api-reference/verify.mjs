// Checks that the generated reference covers the contract exactly: one page per
// operation, one page per area, one sidebar entry each, and the Proctor
// extensions carried through unchanged.

// Every link to a shared type must reach a generated type page.
function typeLinks(fields, found = []) {
  for (const field of fields ?? []) {
    if (field.typeHref) found.push(field.typeHref);
    typeLinks(field.children, found);
    for (const variant of field.variants ?? []) typeLinks(variant.fields, found);
  }
  return found;
}

export function verifyReference({operations, tags, types = []}, files) {
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
  const typeRoutes = new Set(types.map((type) => `/api/reference/types/${type.slug}`));
  for (const type of types) {
    if (!files.has(`types/${type.slug}.mdx`)) failures.push(`${type.name}: type page is missing`);
  }
  const sources = [
    ...operations.map((operation) => [operation.operationId, [operation.pathParams, operation.query, operation.headers, operation.body?.fields, operation.response?.fields].flat().filter(Boolean)]),
    ...types.map((type) => [type.name, type.fields]),
  ];
  for (const [owner, fields] of sources) {
    for (const href of typeLinks(fields)) if (!typeRoutes.has(href)) failures.push(`${owner}: links to ${href}, which has no page`);
  }
  if (failures.length > 0) throw new Error(failures.join('\n'));
  return {operationCount: operations.length, tagCount: tags.length};
}
