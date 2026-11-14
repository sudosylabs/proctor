import assert from 'node:assert/strict';
import test from 'node:test';
import {deflateSync} from 'node:zlib';
import {
  insertApiContractPanel,
  insertProctorContract,
  readGeneratedApi,
} from './openapi-reference-generation.mjs';
import {narrowErrorCodes} from './openapi-reference-input.mjs';
import {verifyOpenapiReference} from './openapi-reference.mjs';

const operation = {
  operationId: 'getLiveness',
  summary: 'Check liveness',
  tags: ['System'],
  security: [],
  'x-proctor-auth': 'public',
  'x-proctor-error-codes': ['system.internal'],
  'x-proctor-idempotency': 'none',
};

const specification = {
  tags: [{name: 'System'}],
  paths: {'/health/live': {get: operation}},
};

const contract = {
  'x-proctor-auth': operation['x-proctor-auth'],
  'x-proctor-error-codes': operation['x-proctor-error-codes'],
  'x-proctor-idempotency': operation['x-proctor-idempotency'],
};

function compressed(value) {
  return deflateSync(JSON.stringify(value)).toString('base64');
}

function generatedPage(overrides = {}, {pageContract = contract} = {}) {
  const api = {
    ...operation,
    method: 'get',
    path: '/health/live',
    extensions: [
      {key: 'x-proctor-auth', value: operation['x-proctor-auth']},
      {key: 'x-proctor-error-codes', value: operation['x-proctor-error-codes']},
      {key: 'x-proctor-idempotency', value: operation['x-proctor-idempotency']},
    ],
    ...overrides,
  };
  return {
    name: 'get-liveness.api.mdx',
    source:
      `---\nid: get-liveness\napi: ${compressed(api)}\n` +
      `proctor_contract: ${JSON.stringify(pageContract)}\n` +
      'hide_send_button: true\n---\n\n<ApiContractPanel />\n',
  };
}

const tagPages = [
  {name: 'system.tag.mdx', source: '---\nid: system\ntitle: "System"\n---\n'},
];
const sidebar = `
  id: "reference/get-liveness",
  className: "api-method get",
`;

test('accepts a complete generated reference', () => {
  assert.deepEqual(
    verifyOpenapiReference({
      specification,
      pages: [generatedPage()],
      tagPages,
      sidebar,
    }),
    {operationCount: 1, tagCount: 1},
  );
});

test('rejects a missing endpoint page', () => {
  assert.throws(
    () => verifyOpenapiReference({specification, pages: [], tagPages, sidebar: ''}),
    /getLiveness: generated endpoint page is missing/,
  );
});

test('rejects changed Proctor metadata', () => {
  const page = generatedPage({
    'x-proctor-auth': 'principal_required',
    extensions: [
      {key: 'x-proctor-auth', value: 'principal_required'},
      {key: 'x-proctor-error-codes', value: operation['x-proctor-error-codes']},
      {key: 'x-proctor-idempotency', value: operation['x-proctor-idempotency']},
    ],
  });

  assert.throws(
    () =>
      verifyOpenapiReference({specification, pages: [page], tagPages, sidebar}),
    /generated x-proctor-auth differs from the canonical operation/,
  );
});

test('places the contract panel after the operation introduction', () => {
  const source =
    '---\nid: example\n---\n\nimport One from "one";\n\n# Title\n\nDescription.\n\n<ParamsDetails>\n</ParamsDetails>\n';
  const result = insertApiContractPanel(source);

  assert.match(
    result,
    /Description\.\n\n<ApiContractPanel \/>\n\n<ParamsDetails>/,
  );
  assert.equal(insertApiContractPanel(result), result);
});

test('rejects generated output without a request-details seam', () => {
  assert.throws(
    () => insertApiContractPanel('---\nid: broken\n---\n\n# Title\n', 'broken.api.mdx'),
    /broken\.api\.mdx: could not locate the generated request-details seam/,
  );
});

function problemDocument(errorCodes) {
  const problem = {$ref: '#/components/schemas/ProblemDetails'};
  return {
    openapi: '3.1.0',
    paths: {
      '/things': {
        parameters: [],
        get: {
          'x-proctor-error-codes': errorCodes,
          responses: {
            200: {description: 'OK', content: {'application/json': {schema: {type: 'object'}}}},
            404: {$ref: '#/components/responses/NotFound'},
            409: {description: 'Conflict', content: {'application/problem+json': {schema: problem}}},
          },
        },
      },
    },
    components: {
      responses: {
        NotFound: {
          description: 'Not found',
          content: {'application/problem+json': {example: {code: 'resource.not_found'}, schema: problem}},
        },
      },
      schemas: {
        ErrorCode: {
          type: 'string',
          description: 'Stable code.',
          enum: ['exam.delivery.metadata_capacity', 'exam.delivery.upload_expired', 'resource.not_found', 'system.internal'],
        },
        ProblemDetails: {
          type: 'object',
          properties: {
            code: {anyOf: [{$ref: '#/components/schemas/ErrorCode'}, {type: 'string'}], type: 'string'},
            title: {type: 'string'},
            delivery: {type: 'object'},
            remaining_bytes: {type: 'integer', description: 'Present only on an exam.delivery.metadata_capacity refusal.'},
          },
        },
      },
    },
  };
}

test('narrows problem responses to the codes each operation declares', () => {
  const source = problemDocument(['resource.not_found']);
  const before = structuredClone(source);
  const output = narrowErrorCodes(source);
  const operation = output.paths['/things'].get;

  for (const status of ['404', '409']) {
    const schema = operation.responses[status].content['application/problem+json'].schema;
    assert.deepEqual(schema.properties.code, {
      type: 'string',
      description: 'Stable code.',
      enum: ['resource.not_found'],
    });
    assert.deepEqual(schema.properties.title, {type: 'string'});
  }
  assert.deepEqual(
    operation.responses['404'].content['application/problem+json'].example,
    {code: 'resource.not_found'},
  );
  assert.deepEqual(operation.responses['200'], before.paths['/things'].get.responses['200']);
  assert.deepEqual(output.components, before.components);
  assert.deepEqual(source, before, 'the server document must not change');
});

test('an operation without error codes keeps a plain code string', () => {
  const output = narrowErrorCodes(problemDocument([]));
  const schema = output.paths['/things'].get.responses['409'].content['application/problem+json'].schema;
  assert.deepEqual(schema.properties.code, {type: 'string', description: 'Stable code.'});
});

test('refuses an operation that declares an unknown error code', () => {
  assert.throws(
    () => narrowErrorCodes(problemDocument(['made.up'])),
    /GET \/things: undeclared error codes made\.up/,
  );
});

test('rejects a contract panel field that differs from the operation', () => {
  const page = generatedPage({}, {pageContract: {...contract, 'x-proctor-idempotency': 'required'}});

  assert.throws(
    () => verifyOpenapiReference({specification, pages: [page], tagPages, sidebar}),
    /getLiveness: proctor_contract x-proctor-idempotency differs from the canonical operation/,
  );
});

test('reads compressed and plain api frontmatter alike', () => {
  const api = {operationId: 'getLiveness', path: '/health/live'};
  assert.deepEqual(readGeneratedApi(`---\napi: ${compressed(api)}\n---\n`), api);
  assert.deepEqual(readGeneratedApi(`---\napi: ${JSON.stringify(api)}\n---\n`), api);
  assert.throws(() => readGeneratedApi('---\nid: x\n---\n', 'x.api.mdx'), /x\.api\.mdx: missing api frontmatter/);
});

test('copies the contract fields beside the compressed operation once', () => {
  const source = `---\nid: get-liveness\napi: ${compressed(operation)}\nhide_send_button: true\n---\n`;
  const result = insertProctorContract(source);

  assert.match(
    result,
    new RegExp(`\\napi: \\S+\\nproctor_contract: ${JSON.stringify(contract).replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\\nhide_send_button`),
  );
  assert.equal(insertProctorContract(result), result);
});

test('keeps refusal-specific extensions only where their codes are declared', () => {
  const extensions = (codes) =>
    Object.keys(
      narrowErrorCodes(problemDocument(codes)).paths['/things'].get.responses['409']
        .content['application/problem+json'].schema.properties,
    ).sort();

  assert.deepEqual(extensions(['resource.not_found']), ['code', 'title']);
  assert.deepEqual(extensions(['exam.delivery.upload_expired']), ['code', 'delivery', 'title']);
  assert.deepEqual(extensions(['exam.delivery.metadata_capacity']), [
    'code',
    'delivery',
    'remaining_bytes',
    'title',
  ]);
});
