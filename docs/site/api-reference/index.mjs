// Endpoint reference pages, built directly from server/openapi.json.
//
// The contract owns every operation, parameter, schema, example and
// x-proctor extension; the error catalog owns what each error code means.
// This module only reshapes them for reading: plain-language constraints,
// errors grouped by status, request samples and schema-based examples.

import {readFile} from 'node:fs/promises';
import {dirname, resolve} from 'node:path';
import {fileURLToPath} from 'node:url';

import {loadProblems} from '../problems/index.mjs';
import {kebab} from '../search/index.mjs';
import {labelFromWords} from './labels.mjs';

const moduleRoot = dirname(fileURLToPath(import.meta.url));
export const defaultRepoRoot = resolve(moduleRoot, '../../..');
export const referenceRoute = '/api/reference';
export const exampleOrigin = 'https://proctor.example.edu';

const methods = ['get', 'post', 'put', 'patch', 'delete', 'options', 'head'];
const idPattern = '^[ybndrfg8ejkmcpqxot1uwisza345h769]{26}$';

const statusLabels = {
  200: 'OK', 201: 'Created', 202: 'Accepted', 204: 'No content',
  400: 'Bad request', 401: 'Unauthorized', 403: 'Forbidden', 404: 'Not found',
  405: 'Method not allowed', 409: 'Conflict', 410: 'Gone', 413: 'Content too large',
  429: 'Too many requests', 500: 'Internal server error', 501: 'Not implemented',
  502: 'Bad gateway', 503: 'Service unavailable',
};

const authenticationLabels = {
  public: 'None',
  principal_required: 'A signed-in caller',
  session_required: 'A signed-in Session',
  recent_session_required: 'A recent signed-in Session',
  strong_recent_session_required: 'A strong, recent signed-in Session',
  refresh_credential_required: 'A refresh credential',
};

const idempotencyLabels = {
  none: 'Not used',
  optional: 'Optional',
  required: 'Required',
};

// Every page links the codes any request can return separately, so endpoint
// lists stay about the endpoint.
export const kernelCodes = ['route.not_found', 'route.method_not_allowed', 'server.internal'];

export {fixedWords, labelFromWords} from './labels.mjs';

export function humanizeOperation(operationId) {
  return labelFromWords(kebab(operationId).split('-'));
}

function sentence(text) {
  const value = String(text ?? '').replace(/\s+/g, ' ').trim();
  if (!value) return '';
  return /[.!?]$/.test(value) ? value : `${value}.`;
}

function meaning(description) {
  return sentence(String(description ?? '').replaceAll('`', '').split('; ')[0].replace(/\.$/, ''));
}

function createResolver(specification) {
  return function resolveSchema(value, depth = 0) {
    let current = value;
    let guard = 0;
    while (current && typeof current === 'object' && current.$ref && guard < 20) {
      let target = specification;
      for (const part of current.$ref.split('/').slice(1)) target = target?.[part];
      current = {...target, ...Object.fromEntries(Object.entries(current).filter(([key]) => key !== '$ref'))};
      guard += 1;
    }
    if (current?.allOf && depth < 8) {
      const merged = {...current, properties: {...(current.properties ?? {})}, required: [...(current.required ?? [])]};
      delete merged.allOf;
      for (const part of current.allOf) {
        const resolved = resolveSchema(part, depth + 1);
        Object.assign(merged.properties, resolved.properties ?? {});
        merged.required.push(...(resolved.required ?? []));
        for (const key of ['type', 'description', 'additionalProperties']) {
          if (merged[key] === undefined && resolved[key] !== undefined) merged[key] = resolved[key];
        }
      }
      return merged;
    }
    return current ?? {};
  };
}

function typeOf(schema) {
  const types = Array.isArray(schema.type) ? schema.type : [schema.type];
  const nullable = types.includes('null') || schema.nullable === true;
  const base = types.find((type) => type && type !== 'null') ?? (schema.properties ? 'object' : schema.anyOf || schema.oneOf ? 'one of' : 'any');
  return {base, nullable};
}

function typeLabel(schema, resolveSchema) {
  const {base, nullable} = typeOf(schema);
  let label = base;
  if (base === 'array') {
    const items = resolveSchema(schema.items ?? {});
    label = `array of ${typeOf(items).base}`;
  } else if (schema.format) {
    label = `${base}, ${schema.format}`;
  }
  return nullable ? `${label} or null` : label;
}

function characterRule(schema) {
  const {minLength: min, maxLength: max} = schema;
  if (min !== undefined && max !== undefined) return min === max ? `${min} characters` : `${min} to ${max} characters`;
  if (min !== undefined) return min === 1 ? 'Not empty' : `At least ${min} characters`;
  if (max !== undefined) return `Up to ${max} characters`;
  return null;
}

// Constraints written for people. The exact pattern stays available beside it.
export function describeRules(schema) {
  const rules = [];
  if (schema.pattern === idPattern) {
    rules.push('26-character Proctor ID');
  } else if (/^\^\[A-Za-z0-9_-\]\{\d+\}\$$/.test(schema.pattern ?? '')) {
    rules.push(`${schema.pattern.match(/\{(\d+)\}/)[1]} characters, URL-safe base64`);
  } else if (/^\^\[0-9a-f\]\{(\d+)\}\$$/.test(schema.pattern ?? '')) {
    rules.push(`${schema.pattern.match(/\{(\d+)\}/)[1]} lowercase hex characters`);
  } else {
    const characters = characterRule(schema);
    if (characters) rules.push(characters);
  }
  const {minimum: lo, maximum: hi, exclusiveMinimum: xlo, exclusiveMaximum: xhi} = schema;
  if (lo !== undefined && hi !== undefined) rules.push(`${lo} to ${hi}`);
  else if (lo !== undefined) rules.push(`${lo} or more`);
  else if (hi !== undefined) rules.push(`Up to ${hi}`);
  if (typeof xlo === 'number') rules.push(`More than ${xlo}`);
  if (typeof xhi === 'number') rules.push(`Less than ${xhi}`);
  if (schema.minItems !== undefined && schema.maxItems !== undefined) rules.push(`${schema.minItems} to ${schema.maxItems} items`);
  else if (schema.maxItems !== undefined) rules.push(`Up to ${schema.maxItems} items`);
  else if (schema.minItems) rules.push(`At least ${schema.minItems} item${schema.minItems === 1 ? '' : 's'}`);
  if (schema.uniqueItems) rules.push('No duplicates');
  if (schema.default !== undefined && typeof schema.default !== 'object') rules.push(`Default ${JSON.stringify(schema.default)}`);
  return rules;
}

function describeField(name, rawSchema, required, resolveSchema, depth = 0) {
  const schema = resolveSchema(rawSchema);
  const items = schema.type === 'array' || (Array.isArray(schema.type) && schema.type.includes('array'))
    ? resolveSchema(schema.items ?? {})
    : null;
  const shape = items && (items.properties || items.allOf) ? items : schema;
  const enumValues = schema.enum ?? items?.enum ?? (schema.const !== undefined ? [schema.const] : null);
  const field = {
    name,
    type: typeLabel(schema, resolveSchema),
    required,
    description: sentence(schema.description ?? ''),
    rules: describeRules(schema),
    pattern: schema.pattern && schema.pattern !== idPattern ? schema.pattern : null,
    values: enumValues ? enumValues.filter((value) => value !== null).map(String) : null,
    children: [],
  };
  if (depth < 4 && shape.properties) {
    const requiredSet = new Set(shape.required ?? []);
    field.children = Object.entries(shape.properties).map(([childName, child]) =>
      describeField(childName, child, requiredSet.has(childName), resolveSchema, depth + 1),
    );
  }
  return field;
}

function describeObject(rawSchema, resolveSchema) {
  const schema = resolveSchema(rawSchema);
  if (schema.type === 'array') {
    return {fields: [describeField('items', schema, true, resolveSchema)], closed: false, list: true};
  }
  const requiredSet = new Set(schema.required ?? []);
  const fields = Object.entries(schema.properties ?? {}).map(([name, child]) =>
    describeField(name, child, requiredSet.has(name), resolveSchema),
  );
  fields.sort((a, b) => Number(b.required) - Number(a.required));
  return {fields, closed: schema.additionalProperties === false, list: false};
}

// Examples: the contract's own example wins; otherwise one is built from the
// schema so it has the right shape and plausible values.
function exampleFromPattern(pattern, seed = 0) {
  const match = pattern?.match(/^\^\[([^\]]+)\]\{(\d+)\}\$$/);
  if (!match) return null;
  const characters = [];
  const set = match[1];
  for (let index = 0; index < set.length; index += 1) {
    if (set[index + 1] === '-' && set[index + 2]) {
      for (let code = set.charCodeAt(index); code <= set.charCodeAt(index + 2); code += 1) characters.push(String.fromCharCode(code));
      index += 2;
    } else {
      characters.push(set[index]);
    }
  }
  return Array.from({length: Number(match[2])}, (_, position) => characters[(position * 7 + 3 + seed) % characters.length]).join('');
}

export function exampleFor(rawSchema, resolveSchema, depth = 0, name = '') {
  const schema = resolveSchema(rawSchema);
  if (schema.example !== undefined) return schema.example;
  if (Array.isArray(schema.examples) && schema.examples.length > 0) return schema.examples[0];
  if (schema.const !== undefined) return schema.const;
  if (schema.default !== undefined) return schema.default;
  if (schema.enum) return schema.enum.find((value) => value !== null) ?? null;
  const {base} = typeOf(schema);
  if (depth > 5) return base === 'array' ? [] : base === 'object' ? {} : null;
  if (base === 'object' || schema.properties) {
    const out = {};
    for (const [key, value] of Object.entries(schema.properties ?? {})) out[key] = exampleFor(value, resolveSchema, depth + 1, key);
    return out;
  }
  if (base === 'array') return [exampleFor(schema.items ?? {}, resolveSchema, depth + 1, name)];
  if (base === 'integer' || base === 'number') {
    if (schema.minimum !== undefined) return Math.max(schema.minimum, schema.minimum === 0 ? 0 : schema.minimum);
    return 1;
  }
  if (base === 'boolean') return true;
  if (base === 'string') {
    if (schema.format === 'date-time') return '2026-06-12T10:58:31Z';
    if (schema.format === 'date') return '2026-06-12';
    if (schema.format === 'email') return 'person@example.edu';
    if (schema.format === 'uri') return 'https://proctor.example.edu/';
    if (schema.format === 'uuid') return '3f6c2a1e-8d4b-4c9a-9e2f-1a7b5c3d9e0f';
    const quoted = String(schema.description ?? '').match(/such as `([^`]+)`/);
    if (quoted) return quoted[1];
    const seed = [...name].reduce((total, character) => total + character.charCodeAt(0), 0);
    const fromPattern = exampleFromPattern(schema.pattern, seed);
    if (fromPattern) return fromPattern;
    return name ? `<${name}>` : 'string';
  }
  return null;
}

function variableName(name) {
  return name.replace(/^X-Proctor-/i, '').replace(/[^A-Za-z0-9]+/g, '_').replace(/^_|_$/g, '').toUpperCase();
}

function camel(name) {
  return variableName(name).toLowerCase().replace(/_([a-z0-9])/g, (_, letter) => letter.toUpperCase());
}

function snake(name) {
  return variableName(name).toLowerCase();
}

// Request samples in four forms. Values the caller must supply are variables;
// body values come from the contract's example.
export function requestSamples(operation) {
  const {method, path, auth, headers, query, body} = operation;
  const upper = method.toUpperCase();
  const requiredQuery = query.filter((parameter) => parameter.required);
  const curlPath = path.replace(/\{([^}]+)\}/g, (_, name) => `$${variableName(name)}`);
  const jsPath = path.replace(/\{([^}]+)\}/g, (_, name) => `\${${camel(name)}}`);
  const pyPath = path.replace(/\{([^}]+)\}/g, (_, name) => `{${snake(name)}}`);
  const queryString = (style) => requiredQuery.length === 0 ? '' : `?${requiredQuery.map((parameter) => `${parameter.name}=${style(parameter.name)}`).join('&')}`;
  const authHeader = auth === 'public' ? [] : [['Authorization', 'Bearer', 'ACCESS_TOKEN']];
  const headerList = [
    ...authHeader.map(([name, scheme, variable]) => ({name, curl: `${scheme} $${variable}`, js: `\`${scheme} \${accessToken}\``, py: `f"${scheme} {access_token}"`, http: `${scheme} <access token>`})),
    ...headers.filter((header) => header.required).map((header) => ({
      name: header.name,
      curl: `$${variableName(header.name)}`,
      js: camel(header.name),
      py: snake(header.name),
      http: `<${header.name.replace(/^X-Proctor-/i, '').replace(/-/g, ' ').toLowerCase()}>`,
    })),
  ];
  const json = body?.contentType === 'application/json' ? JSON.stringify(body.example ?? {}, null, 2) : null;
  const curl = [`curl -X ${upper} "$PROCTOR${curlPath}${queryString((name) => `$${variableName(name)}`)}"`];
  for (const header of headerList) curl.push(`  -H "${header.name}: ${header.curl}"`);
  let curlBody = [];
  if (json) {
    curlBody = [`  --json '${json.split('\n')[0]}`, ...json.split('\n').slice(1).map((line) => `  ${line}`)];
    curlBody[curlBody.length - 1] += "'";
  } else if (body?.contentType === 'multipart/form-data') {
    // curl sets the multipart Content-Type and its boundary itself.
    curlBody = body.parts.filter((part) => part.required || part.file).map((part, index, all) => {
      const value = part.file
        ? `"${part.name}=@path/to/file"`
        : part.example !== null && typeof part.example === 'object'
          ? `'${part.name}=${JSON.stringify(part.example)};type=application/json'`
          : `"${part.name}=${String(part.example ?? '')}"`;
      return `  -F ${value}${index < all.length - 1 ? ' \\' : ''}`;
    });
  } else if (body) {
    curl.push(`  -H "Content-Type: ${body.contentType}"`);
    curlBody = ['  --data-binary "@path/to/file"'];
  }
  const curlLines = curl.map((line, index) => (index < curl.length - 1 || curlBody.length ? `${line} \\` : line));
  const curlText = [...curlLines, ...curlBody].join('\n');

  const jsLines = [
    `const res = await fetch(\`\${proctor}${jsPath}${queryString((name) => `\${${camel(name)}}`)}\`, {`,
    `  method: "${upper}",`,
  ];
  if (headerList.length || json) {
    jsLines.push('  headers: {');
    for (const header of headerList) jsLines.push(`    ${/^[A-Za-z]+$/.test(header.name) ? header.name : `"${header.name}"`}: ${header.js},`);
    if (json) jsLines.push('    "Content-Type": "application/json",');
    jsLines.push('  },');
  }
  const jsBodyStart = jsLines.length;
  if (json) {
    const bodyLines = json.split('\n');
    jsLines.push(`  body: JSON.stringify(${bodyLines[0]}`, ...bodyLines.slice(1, -1).map((line) => `  ${line}`), `  ${bodyLines.at(-1)}),`);
  } else if (body?.contentType === 'multipart/form-data') {
    jsLines.push('  body: form, // FormData with ' + body.parts.map((part) => part.name).join(', '));
  } else if (body) {
    jsLines.push('  body: file,');
  }
  const jsBodyEnd = jsLines.length;
  jsLines.push('});');

  const pyLines = [`res = requests.${method === 'delete' ? 'delete' : method}(`, `    f"{proctor}${pyPath}"${requiredQuery.length ? ',' : ','}`];
  if (requiredQuery.length) pyLines.push(`    params={${requiredQuery.map((parameter) => `"${parameter.name}": ${snake(parameter.name)}`).join(', ')}},`);
  if (headerList.length) {
    pyLines.push('    headers={');
    for (const header of headerList) pyLines.push(`        "${header.name}": ${header.py},`);
    pyLines.push('    },');
  }
  const pyBodyStart = pyLines.length;
  if (json) {
    const pyJson = json.replace(/\btrue\b/g, 'True').replace(/\bfalse\b/g, 'False').replace(/\bnull\b/g, 'None').split('\n');
    pyLines.push(`    json=${pyJson[0]}`, ...pyJson.slice(1, -1).map((line) => `    ${line}`), `    ${pyJson.at(-1)},`);
  } else if (body?.contentType === 'multipart/form-data') {
    const isJson = (part) => part.example !== null && typeof part.example === 'object';
    const files = body.parts.filter((part) => part.file || (part.required && isJson(part)));
    const values = body.parts.filter((part) => !part.file && !isJson(part) && part.required);
    if (files.length) {
      pyLines.push(`    files={${files.map((part) => (part.file
        ? `"${part.name}": open("path/to/file", "rb")`
        : `"${part.name}": (None, json.dumps(${JSON.stringify(part.example).replace(/\btrue\b/g, 'True').replace(/\bfalse\b/g, 'False').replace(/\bnull\b/g, 'None')}), "application/json")`)).join(', ')}},`);
    }
    if (values.length) pyLines.push(`    data={${values.map((part) => `"${part.name}": ${JSON.stringify(part.example ?? '')}`).join(', ')}},`);
  } else if (body) {
    pyLines.push('    data=open("path/to/file", "rb"),');
  }
  const pyBodyEnd = pyLines.length;
  pyLines.push(')');

  const httpLines = [`${upper} ${path}${requiredQuery.length ? `?${requiredQuery.map((parameter) => `${parameter.name}={${parameter.name}}`).join('&')}` : ''} HTTP/1.1`, 'Host: proctor.example.edu'];
  for (const header of headerList) httpLines.push(`${header.name}: ${header.http}`);
  if (body) httpLines.push(`Content-Type: ${body.contentType}${body.contentType === 'multipart/form-data' ? '; boundary=<boundary>' : ''}`);
  const httpBodyStart = httpLines.length + 1;
  if (json) httpLines.push('', ...json.split('\n'));
  const httpBodyEnd = httpLines.length;

  const range = (start, end) => (end > start ? `{${start + 1}-${end}}` : '');
  const curlBodyStart = curlLines.length;
  return [
    {id: 'curl', label: 'curl', language: 'bash', code: curlText, highlight: json ? range(curlBodyStart, curlBodyStart + curlBody.length) : ''},
    {id: 'js', label: 'JavaScript', language: 'javascript', code: jsLines.join('\n'), highlight: json ? range(jsBodyStart, jsBodyEnd) : ''},
    {id: 'python', label: 'Python', language: 'python', code: pyLines.join('\n'), highlight: json ? range(pyBodyStart, pyBodyEnd) : ''},
    {id: 'http', label: 'HTTP', language: 'http', code: httpLines.join('\n'), highlight: json ? range(httpBodyStart, httpBodyEnd) : ''},
  ];
}

function describeParameter(raw, resolveSchema) {
  const parameter = resolveSchema(raw);
  const schema = resolveSchema(parameter.schema ?? {});
  const field = describeField(parameter.name, schema, Boolean(parameter.required), resolveSchema);
  field.description = sentence(parameter.description ?? schema.description ?? '');
  return {...field, in: parameter.in};
}

function problemExample(problem, path) {
  return JSON.stringify({
    type: `https://docs.example.edu/problems/${problem.code}`,
    title: problem.title,
    status: problem.status,
    code: problem.code,
    request_id: 'KJ4RTMQZ7XWB6N3HDYF5VCPAGE',
  }, null, 2);
}

export async function loadReference({repoRoot = defaultRepoRoot} = {}) {
  const specification = JSON.parse(await readFile(resolve(repoRoot, 'server/openapi.json'), 'utf8'));
  const problems = new Map((await loadProblems({repoRoot})).map((problem) => [problem.code, problem]));
  const resolveSchema = createResolver(specification);
  const operations = [];

  for (const [path, item] of Object.entries(specification.paths ?? {})) {
    const shared = item.parameters ?? [];
    for (const method of methods) {
      const operation = item[method];
      if (!operation?.operationId) continue;
      const parameters = [...shared, ...(operation.parameters ?? [])].map((parameter) => describeParameter(parameter, resolveSchema));
      const auth = operation['x-proctor-auth'] ?? 'public';
      const idempotency = operation['x-proctor-idempotency'] ?? 'none';

      let body = null;
      if (operation.requestBody) {
        const requestBody = resolveSchema(operation.requestBody);
        const [contentType, media] = Object.entries(requestBody.content ?? {})[0] ?? [];
        if (contentType) {
          const structured = contentType === 'application/json' || contentType === 'multipart/form-data';
          const described = structured ? describeObject(media.schema ?? {}, resolveSchema) : {fields: [], closed: false, list: false};
          const example = media.example ?? Object.values(media.examples ?? {})[0]?.value ?? (contentType === 'application/json' ? exampleFor(media.schema ?? {}, resolveSchema) : null);
          // Multipart bodies are sent as named parts: files by path, the rest as values.
          const formSchema = resolveSchema(media.schema ?? {});
          const parts = contentType === 'multipart/form-data'
            ? Object.entries(formSchema.properties ?? {}).map(([name, raw]) => {
                const part = resolveSchema(raw);
                const file = part.format === 'binary' || part.contentMediaType !== undefined || part.contentEncoding === 'binary';
                return {name, file, required: (formSchema.required ?? []).includes(name), example: file ? null : exampleFor(part, resolveSchema, 0, name)};
              })
            : [];
          body = {contentType, required: Boolean(requestBody.required), description: sentence(requestBody.description ?? ''), ...described, example, parts};
        }
      }

      const responseEntries = Object.entries(operation.responses ?? {});
      const successEntry = responseEntries.find(([status]) => status.startsWith('2'));
      let response = null;
      let responseExample = null;
      if (successEntry) {
        const [status, raw] = successEntry;
        const resolved = resolveSchema(raw);
        const [contentType, media] = Object.entries(resolved.content ?? {})[0] ?? [];
        const described = contentType === 'application/json' && media?.schema ? describeObject(media.schema, resolveSchema) : {fields: [], closed: false, list: false};
        response = {status: Number(status), label: statusLabels[status] ?? '', contentType: contentType ?? null, description: sentence(resolved.description ?? ''), ...described};
        if (contentType === 'application/json' && media?.schema) {
          const example = media.example ?? Object.values(media.examples ?? {})[0]?.value ?? exampleFor(media.schema, resolveSchema);
          responseExample = {status: Number(status), label: statusLabels[status] ?? '', ok: true, code: JSON.stringify(example, null, 2), fromContract: media.example !== undefined || media.examples !== undefined};
        }
      }

      const groups = new Map();
      for (const code of operation['x-proctor-error-codes'] ?? []) {
        const problem = problems.get(code);
        const status = problem?.status ?? 0;
        if (!groups.has(status)) groups.set(status, []);
        groups.get(status).push({code, meaning: problem ? meaning(problem.description) : ''});
      }
      const errors = [...groups.entries()].sort(([a], [b]) => a - b).map(([status, codes]) => ({status, label: statusLabels[status] ?? '', codes}));

      const examples = [];
      if (responseExample) examples.push(responseExample);
      for (const preferred of [409, 503, 400]) {
        const group = errors.find((candidate) => candidate.status === preferred);
        const domainCode = group?.codes.find((entry) => !entry.code.startsWith('authentication.') && !entry.code.startsWith('idempotency.') && !entry.code.startsWith('request.')) ?? group?.codes[0];
        if (domainCode && examples.length < 3) {
          examples.push({status: preferred, label: statusLabels[preferred], ok: false, code: problemExample(problems.get(domainCode.code), path), errorCode: domainCode.code});
        }
      }

      const model = {
        id: kebab(operation.operationId),
        operationId: operation.operationId,
        name: humanizeOperation(operation.operationId),
        method,
        path,
        tag: operation.tags?.[0] ?? 'Other',
        tagSlug: kebab(operation.tags?.[0] ?? 'Other'),
        summary: sentence(operation.summary ?? ''),
        description: sentence(operation.description ?? ''),
        auth,
        authLabel: authenticationLabels[auth] ?? auth.replaceAll('_', ' '),
        idempotency,
        idempotencyLabel: idempotencyLabels[idempotency] ?? idempotency,
        pathParams: parameters.filter((parameter) => parameter.in === 'path'),
        query: parameters.filter((parameter) => parameter.in === 'query'),
        headers: parameters.filter((parameter) => parameter.in === 'header'),
        cookies: parameters.filter((parameter) => parameter.in === 'cookie'),
        body,
        response,
        errors,
        errorCount: errors.reduce((total, group) => total + group.codes.length, 0),
        examples,
        extensions: {
          'x-proctor-auth': operation['x-proctor-auth'],
          'x-proctor-idempotency': operation['x-proctor-idempotency'],
          'x-proctor-error-codes': operation['x-proctor-error-codes'],
        },
      };
      model.samples = requestSamples(model);
      operations.push(model);
    }
  }

  const tags = (specification.tags ?? []).map((tag) => ({
    name: tag.name,
    slug: kebab(tag.name),
    description: sentence(tag.description ?? ''),
    operations: operations.filter((operation) => operation.tag === tag.name).map(({id, name, method, path, summary}) => ({id, name, method, path, summary})),
  }));
  for (const operation of operations) {
    operation.more = (tags.find((tag) => tag.name === operation.tag)?.operations ?? []).filter((other) => other.id !== operation.id);
  }
  return {operations, tags, kernel: kernelCodes.map((code) => ({code, meaning: meaning(problems.get(code)?.description)}))};
}

function frontMatter(fields) {
  return `---\n${Object.entries(fields).map(([key, value]) => `${key}: ${JSON.stringify(value)}`).join('\n')}\n---\n`;
}

// Every file of the generated reference root, keyed by its path in it.
export function renderReferenceFiles({operations, tags, kernel}) {
  const files = new Map();
  for (const operation of operations) {
    files.set(`${operation.id}.json`, `${JSON.stringify({...operation, kernel})}\n`);
    files.set(`${operation.id}.mdx`, `${frontMatter({
      id: operation.id,
      title: operation.name,
      description: operation.summary,
      sidebar_label: operation.name,
      sidebar_class_name: `api-ep api-ep--${operation.method}`,
      hide_title: true,
      hide_table_of_contents: true,
      pagination_prev: null,
      pagination_next: null,
      custom_edit_url: null,
      api: {operationId: operation.operationId, method: operation.method, path: operation.path, tags: [operation.tag], ...operation.extensions},
    })}
import EndpointPage from '@site/src/components/EndpointPage';
import data from './${operation.id}.json';

<EndpointPage data={data} />
`);
  }
  for (const tag of tags) {
    files.set(`${tag.slug}.area.json`, `${JSON.stringify(tag)}\n`);
    files.set(`${tag.slug}.mdx`, `${frontMatter({
      id: tag.slug,
      title: tag.name,
      description: tag.description,
      hide_title: true,
      hide_table_of_contents: true,
      pagination_prev: null,
      pagination_next: null,
      custom_edit_url: null,
    })}
import AreaPage from '@site/src/components/AreaPage';
import data from './${tag.slug}.area.json';

<AreaPage data={data} />
`);
  }
  const sidebar = tags.map((tag) => ({
    type: 'category',
    label: tag.name,
    link: {type: 'doc', id: `reference/${tag.slug}`},
    items: tag.operations.map((operation) => ({
      type: 'doc',
      id: `reference/${operation.id}`,
      label: operation.name,
      className: `api-ep api-ep--${operation.method}`,
    })),
  }));
  files.set('sidebar.ts', `// Generated from server/openapi.json. Do not edit by hand.\nimport type {SidebarsConfig} from '@docusaurus/plugin-content-docs';\n\nconst sidebar: SidebarsConfig[string] = ${JSON.stringify(sidebar, null, 2)};\n\nexport default sidebar;\n`);
  return files;
}
