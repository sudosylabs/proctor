// The API reference generator inlines every schema into every page that uses
// it. ProblemDetails carries the ErrorCode enum of every code in the product,
// so each error response on each page repeated hundreds of codes the
// operation can never return, along with delivery-only extensions, and the
// docs build ran out of memory. The reference input narrows each operation's
// problem responses to the codes it declares in x-proctor-error-codes and the
// extensions those codes carry. The published openapi.json download stays the
// server's document, unchanged.

const problemDetailsRef = '#/components/schemas/ProblemDetails';
const httpMethods = new Set([
  'get',
  'put',
  'post',
  'delete',
  'options',
  'head',
  'patch',
  'trace',
]);

function resolveResponse(document, response) {
  const ref = response?.$ref;
  if (typeof ref !== 'string') {
    return response;
  }
  const name = ref.replace('#/components/responses/', '');
  const shared = document.components?.responses?.[name];
  if (shared === undefined) {
    throw new Error(`unknown shared response ${ref}`);
  }
  return shared;
}

// Problem extensions that only some refusals carry. A field documented as
// "Present only on an <code> refusal" stays where the operation declares that
// code; the delivery recovery object stays where it declares a delivery code.
const onlyOnRefusal = /^Present only on an? ([a-z0-9_.]+) refusal\.$/;
const deliveryCodePrefix = 'exam.delivery.';

function extensionApplies(name, property, codes) {
  if (name === 'delivery') {
    return codes.some((code) => code.startsWith(deliveryCodePrefix));
  }
  const refusal = property.description?.match(onlyOnRefusal)?.[1];
  return refusal === undefined || codes.includes(refusal);
}

function narrowedProblemDetails(problemDetails, errorCode, codes) {
  const properties = Object.fromEntries(
    Object.entries(problemDetails.properties).filter(([name, property]) =>
      extensionApplies(name, property, codes),
    ),
  );
  return {
    ...problemDetails,
    properties: {
      ...properties,
      code:
        codes.length === 0
          ? {type: 'string', description: errorCode.description}
          : {type: 'string', description: errorCode.description, enum: codes},
    },
  };
}

export function narrowErrorCodes(document) {
  const problemDetails = document.components?.schemas?.ProblemDetails;
  const errorCode = document.components?.schemas?.ErrorCode;
  if (problemDetails?.properties?.code === undefined || errorCode === undefined) {
    throw new Error('ProblemDetails.code and ErrorCode must exist to narrow error codes');
  }
  const known = new Set(errorCode.enum);
  const output = structuredClone(document);

  for (const [path, pathItem] of Object.entries(output.paths)) {
    for (const [method, operation] of Object.entries(pathItem)) {
      if (!httpMethods.has(method)) {
        continue;
      }
      const location = `${method.toUpperCase()} ${path}`;
      const codes = operation['x-proctor-error-codes'];
      if (!Array.isArray(codes)) {
        throw new Error(`${location}: x-proctor-error-codes must be an array`);
      }
      const unknown = codes.filter((code) => !known.has(code));
      if (unknown.length > 0) {
        throw new Error(`${location}: undeclared error codes ${unknown.join(', ')}`);
      }
      const schema = narrowedProblemDetails(problemDetails, errorCode, codes);

      for (const [status, response] of Object.entries(operation.responses ?? {})) {
        const resolved = resolveResponse(output, response);
        const content = resolved?.content ?? {};
        const problemTypes = Object.keys(content).filter(
          (type) => content[type]?.schema?.$ref === problemDetailsRef,
        );
        if (problemTypes.length === 0) {
          continue;
        }
        const narrowed = {...resolved, content: {...content}};
        for (const type of problemTypes) {
          narrowed.content[type] = {...content[type], schema};
        }
        operation.responses[status] = narrowed;
      }
    }
  }
  return output;
}
