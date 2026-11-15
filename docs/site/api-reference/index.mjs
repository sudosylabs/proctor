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
  101: 'Switching protocols', 302: 'Found', 303: 'See other', 307: 'Temporary redirect',
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
  const base = types.find((type) => type && type !== 'null')
    ?? (schema.properties ? 'object' : schema.anyOf || schema.oneOf ? 'one of' : 'any');
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

// Describes what a character class allows, for patterns like ^[a-z0-9._-]{1,64}$.
function describeClass(set) {
  const parts = [];
  let rest = set;
  const take = (token, label) => {
    if (rest.includes(token)) {
      parts.push(label);
      rest = rest.replace(token, '');
    }
  };
  take('A-Za-z', 'letters');
  take('A-Z', 'capital letters');
  take('a-z', 'lowercase letters');
  take('0-9', 'digits');
  const symbols = rest.replace(/\\/g, '').split('').filter(Boolean);
  if (symbols.length) parts.push(symbols.join(' '));
  return parts.join(', ').replace(/, ([^,]*)$/, ' and $1');
}

function repetition(quantifier) {
  if (quantifier === '+') return {min: 1, max: null};
  const match = quantifier.match(/^\{(\d+)(?:,(\d*))?\}$/);
  if (!match) return null;
  const min = Number(match[1]);
  const max = match[2] === undefined ? min : match[2] === '' ? null : Number(match[2]);
  return {min, max};
}

function lengthPhrase(min, max) {
  if (max === null) return min <= 1 ? 'at least 1 character' : `at least ${min} characters`;
  return min === max ? `${min} characters` : `${min} to ${max} characters`;
}

// Common formats, then any pattern made of one or two character classes.
export function patternRule(pattern) {
  if (!pattern) return null;
  const known = {
    '^[ybndrfg8ejkmcpqxot1uwisza345h769]{26}$': '26-character Proctor ID',
    '^$|^[ybndrfg8ejkmcpqxot1uwisza345h769]{26}$': '26-character Proctor ID, or empty',
    '^[0-9A-HJKMNP-TV-Z]{26}$': '26-character ID in Crockford base32 (capital letters and digits)',
    '^[0-9A-HJKMNP-TV-Z]{4}(-[0-9A-HJKMNP-TV-Z]{4}){3}$': 'Four groups of four capital letters or digits, such as MBY3-KBMN-EB83-TFW1',
    '^sha256:[0-9a-f]{64}$': 'sha256: followed by 64 lowercase hex characters',
    '^fnv1a64:[0-9a-f]{16}$': 'fnv1a64: followed by 16 lowercase hex characters',
    '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$': 'A version 4 UUID in lowercase',
  };
  if (known[pattern]) return known[pattern];
  if (/^\^\[A-Za-z0-9_-\]\{\d+\}\$$/.test(pattern)) return `${pattern.match(/\{(\d+)\}/)[1]} characters, URL-safe base64`;
  if (/^\^\[0-9a-f\]\{\d+\}\$$/.test(pattern)) return `${pattern.match(/\{(\d+)\}/)[1]} lowercase hex characters`;
  if (/Z\$$/.test(pattern)) return 'A UTC time ending in Z';
  let match = pattern.match(/^\^\[([^\]]+)\](\{\d+(?:,\d*)?\}|\+)\$$/);
  if (match) {
    const count = repetition(match[2]);
    if (count) return `${lengthPhrase(count.min, count.max).replace(/^./, (c) => c.toUpperCase())}: ${describeClass(match[1])}`;
  }
  match = pattern.match(/^\^\[([^\]]+)\]\[([^\]]+)\](\{\d+(?:,\d*)?\})\$$/);
  if (match) {
    const count = repetition(match[3]);
    if (count) {
      const max = count.max === null ? null : count.max + 1;
      return `${lengthPhrase(count.min + 1, max).replace(/^./, (c) => c.toUpperCase())}: ${describeClass(match[2])}, starting with ${describeClass(match[1]).replace(/^letters/, 'a letter').replace(/^lowercase letters/, 'a lowercase letter').replace(/ and digits$/, ' or digit').replace(/ and /, ' or ')}`;
    }
  }
  return null;
}

// Constraints written for people. The exact pattern stays available beside it.
export function describeRules(schema) {
  const rules = [];
  const fromPattern = patternRule(schema.pattern);
  if (fromPattern) {
    rules.push(fromPattern);
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

function refName(raw) {
  return raw && typeof raw === 'object' && typeof raw.$ref === 'string' ? raw.$ref.split('/').pop() : null;
}

function isArray(schema) {
  return schema.type === 'array' || (Array.isArray(schema.type) && schema.type.includes('array'));
}

// A closed catalogue in a response is written as anyOf [catalogue, string] so
// older clients accept newer values. That is one string with known values,
// not a choice of shapes.
function catalogueValues(schema, resolveSchema) {
  const options = schema.anyOf ?? schema.oneOf;
  if (!options) return null;
  const resolved = options.map((option) => resolveSchema(option));
  if (resolved.some((option) => option.properties || isArray(option))) return null;
  const values = resolved.flatMap((option) => option.enum ?? []);
  return values.length ? {values, open: resolved.some((option) => !option.enum && typeOf(option).base === 'string')} : null;
}

// Fields that mean the same thing everywhere get one line and a guide link.
export function standardField(name, place) {
  if (place === 'header' && name === 'Idempotency-Key') {
    return {text: 'Makes the request safe to retry: the same key and request get the same result.', href: '/api/guides/idempotency', label: 'How idempotency works'};
  }
  if (place === 'query' && name === 'cursor') {
    return {text: 'Continues a listing: send the next_cursor from the previous page, with the same query.', href: '/api/guides/pagination', label: 'How paging works'};
  }
  if (place === 'query' && name === 'limit') {
    return {text: 'The most items to return in this page.', href: '/api/guides/pagination', label: 'How paging works'};
  }
  if (place === 'response' && name === 'next_cursor') {
    return {text: 'Present when there are more results. Send it as cursor to get the next page.', href: '/api/guides/pagination', label: 'How paging works'};
  }
  return null;
}

// Builds the readable model of one field. Named object types that are large
// or reused become links to their own page instead of being expanded again.
export function describeField(name, rawSchema, required, context, depth = 0) {
  const {resolveSchema} = context;
  const schema = resolveSchema(rawSchema);
  // "One of X or null" is X that may be null, not a choice of shapes.
  const choice = schema.oneOf ?? schema.anyOf;
  if (choice && !schema.properties) {
    const present = choice.filter((option) => resolveSchema(option).type !== 'null');
    if (present.length === 1 && present.length < choice.length) {
      const inner = describeField(name, {...present[0], ...(schema.description ? {description: schema.description} : {})}, required, context, depth);
      if (!inner.type.endsWith('or null')) inner.type += ' or null';
      return inner;
    }
  }
  const items = isArray(schema) ? resolveSchema(schema.items ?? {}) : null;
  const typeName = isArray(schema) ? refName(schema.items) : refName(rawSchema);
  const shape = items && (items.properties || items.allOf || items.oneOf || items.anyOf) ? items : schema;
  const catalogue = catalogueValues(shape, resolveSchema);
  const enumValues = schema.enum ?? items?.enum ?? (schema.const !== undefined ? [schema.const] : null) ?? catalogue?.values ?? null;
  const field = {
    name,
    type: typeLabel(schema, resolveSchema),
    typeName: null,
    typeHref: null,
    required,
    description: sentence(rawSchema?.description ?? schema.description ?? ''),
    rules: describeRules(schema),
    pattern: schema.pattern && !['^[ybndrfg8ejkmcpqxot1uwisza345h769]{26}$'].includes(schema.pattern) ? schema.pattern : null,
    values: enumValues ? enumValues.filter((value) => value !== null).map(String) : null,
    valuesOpen: Boolean(catalogue?.open),
    children: [],
    variants: [],
    map: false,
    standard: null,
  };
  if (catalogue) field.type = isArray(schema) ? 'array of string' : 'string';

  const objectShape = Boolean(shape.properties) && !catalogue;
  if (typeName && objectShape) {
    field.typeName = typeName;
    field.type = isArray(schema) ? `array of ${typeName}` : typeName;
    if (typeOf(schema).nullable) field.type += ' or null';
    if (context.shouldLink(typeName, shape) || context.trail.has(typeName)) {
      field.typeHref = context.link(typeName, shape);
      return field;
    }
  }

  // A map: arbitrary keys, each holding the same kind of value.
  if (!shape.properties && shape.additionalProperties && typeof shape.additionalProperties === 'object') {
    const value = resolveSchema(shape.additionalProperties);
    const valueName = refName(shape.additionalProperties);
    field.map = true;
    field.type = `map of ${valueName && value.properties ? valueName : typeLabel(value, resolveSchema)}`;
    if (depth < 4 && value.properties) {
      field.children = [describeField('{key}', shape.additionalProperties, true, context, depth + 1)];
    }
    return field;
  }

  // Rules that depend on another member, such as "when enabled is true".
  const options = !catalogue ? shape.oneOf ?? shape.anyOf : null;
  if (options && shape.properties) {
    for (const option of options.map((value) => resolveSchema(value))) {
      const when = Object.entries(option.properties ?? {}).find(([, value]) => value.const !== undefined);
      if (when && option.required?.length) {
        field.rules.push(`When ${when[0]} is ${JSON.stringify(when[1].const)}: ${option.required.filter((key) => key !== when[0]).join(' and ')} required`);
      } else if (when && option.not) {
        field.rules.push(`When ${when[0]} is ${JSON.stringify(when[1].const)}: leave the other members out`);
      }
    }
  }

  // A choice of shapes: list each variant's fields under its own label.
  if (options && !shape.properties && depth < 4) {
    const resolvedOptions = options.map((option) => resolveSchema(option));
    const discriminator = Object.keys(resolvedOptions[0]?.properties ?? {}).find((key) =>
      resolvedOptions.every((option) => option.properties?.[key]?.const !== undefined));
    field.type = `${isArray(schema) ? 'array of ' : ''}one of ${options.length} shapes`;
    field.discriminator = discriminator ?? null;
    field.variants = options.map((option, index) => {
      const resolved = resolvedOptions[index];
      const label = discriminator ? `${discriminator}: ${JSON.stringify(resolved.properties[discriminator].const)}` : refName(option) ?? resolved.title ?? `Shape ${index + 1}`;
      const requiredSet = new Set(resolved.required ?? []);
      return {
        label,
        description: sentence(resolved.description ?? ''),
        fields: Object.entries(resolved.properties ?? {}).map(([childName, child]) =>
          describeField(childName, child, requiredSet.has(childName), context, depth + 1),
        ),
      };
    });
    return field;
  }

  if (depth < 5 && shape.properties) {
    const requiredSet = new Set(shape.required ?? []);
    if (typeName) context.trail.add(typeName);
    field.children = Object.entries(shape.properties).map(([childName, child]) =>
      describeField(childName, child, requiredSet.has(childName), context, depth + 1),
    );
    if (typeName) context.trail.delete(typeName);
  }
  return field;
}

export function describeObject(rawSchema, context, place = 'body') {
  const schema = context.resolveSchema(rawSchema);
  if (isArray(schema)) {
    return {fields: [describeField('items', rawSchema, true, context)], closed: false, list: true};
  }
  const requiredSet = new Set(schema.required ?? []);
  const fields = Object.entries(schema.properties ?? {}).map(([name, child]) => {
    const field = describeField(name, child, requiredSet.has(name), context);
    field.standard = standardField(name, place);
    if (field.standard && /cursor/.test(name)) field.rules = [];
    return field;
  });
  return {fields, closed: schema.additionalProperties === false, list: false};
}

// Examples: the contract's own example wins; otherwise one is built from the
// schema and the field's name so it reads like real data.
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

const namedStrings = [
  [/cursor$/, 'djE6c2l0dGluZ3M6MDAwMDQy'],
  [/markdown$/, 'Answer every question. You may use the provided reference sheet.'],
  [/^username$/, 'candidate.0142'],
  [/(^|_)(notes|rationale|remarks)$/, 'Reviewed against the Sitting log.'],
  [/token$/, 'pat_7Kq2mX9vR4tL8wNcY3bF6hJd'],
  [/masked_recipient$/, 'c•••@example.edu'],
  [/message_id$/, '<20260612105831.4f2a@proctor.example.edu>'],
  [/^subject$/, 'Introduction to Databases final exam'],
  [/authentication_method$/, 'password'],
  [/authentication_strength$/, 'strong'],
  [/^(display_)?name$|_name$|^title$/, 'Introduction to Databases'],
  [/email/, 'person@example.edu'],
  [/^reason$|_reason$|^note$|_note$|^remark/, 'Requested by the Exam Manager.'],
  [/description$|^summary$/, 'A short description.'],
  [/^q$|query$/, 'databases'],
  [/^path$|_path$/, 'src/main.py'],
  [/media_type$|content_type$/, 'text/plain'],
  [/locale$|language$/, 'en'],
  [/time_?zone$/, 'Europe/London'],
  [/url$|uri$/, 'https://proctor.example.edu/'],
  [/(^|_)code$/, 'ABC-123'],
  [/version$/, '1.4.0'],
  [/^key$|_key$/, 'item-001'],
];

export function exampleFor(rawSchema, resolveSchema, depth = 0, name = '') {
  const schema = resolveSchema(rawSchema);
  if (schema.example !== undefined) return schema.example;
  if (Array.isArray(schema.examples) && schema.examples.length > 0) return schema.examples[0];
  if (schema.const !== undefined) return schema.const;
  if (schema.default !== undefined) return schema.default;
  if (schema.enum) return schema.enum.find((value) => value !== null) ?? null;
  const catalogue = catalogueValues(schema, resolveSchema);
  if (catalogue) return catalogue.values[0];
  if ((schema.oneOf ?? schema.anyOf) && !schema.properties) return exampleFor((schema.oneOf ?? schema.anyOf)[0], resolveSchema, depth + 1, name);
  const {base} = typeOf(schema);
  if (depth > 5) return base === 'array' ? [] : base === 'object' ? {} : null;
  if (base === 'object' || schema.properties) {
    if (!schema.properties && schema.additionalProperties && typeof schema.additionalProperties === 'object') {
      return {'example-key': exampleFor(schema.additionalProperties, resolveSchema, depth + 1, name)};
    }
    const out = {};
    for (const [key, value] of Object.entries(schema.properties ?? {})) out[key] = exampleFor(value, resolveSchema, depth + 1, key);
    return out;
  }
  if (base === 'array') return [exampleFor(schema.items ?? {}, resolveSchema, depth + 1, name)];
  if (base === 'integer' || base === 'number') {
    if (schema.minimum !== undefined) return schema.minimum;
    return /count|total|size|limit/.test(name) ? 12 : 1;
  }
  if (base === 'boolean') return true;
  if (base === 'string') {
    if (schema.format === 'date-time' || /Z\$$/.test(schema.pattern ?? '')) return '2026-06-12T10:58:31Z';
    if (schema.format === 'date') return '2026-06-12';
    if (schema.format === 'email') return 'person@example.edu';
    if (schema.format === 'uri') return 'https://proctor.example.edu/';
    if (schema.format === 'uuid' || /^\^\[0-9a-f\]\{8\}-/.test(schema.pattern ?? '')) return '3f6c2a1e-8d4b-4c9a-9e2f-1a7b5c3d9e0f';
    const quoted = String(schema.description ?? '').match(/such as `([^`]+)`/);
    if (quoted) return quoted[1];
    if (schema.pattern === '^sha256:[0-9a-f]{64}$') return `sha256:${'9f2c41e7b05a83d6'.repeat(4)}`;
    if (schema.pattern === '^fnv1a64:[0-9a-f]{16}$') return 'fnv1a64:9f2c41e7b05a83d6';
    const seed = [...name].reduce((total, character) => total + character.charCodeAt(0), 0);
    const fromPattern = exampleFromPattern(schema.pattern, seed);
    if (fromPattern) return fromPattern;
    for (const [test, value] of namedStrings) if (test.test(name)) return value;
    // Identifiers and other tokens: a readable value made from the field's name.
    const token = name.replace(/_(id|ref|refs|ids)$/, '').replaceAll('_', '-') || 'value';
    if (schema.pattern || /(^|_)(id|ref|refs|revision|reference|release)$/.test(name)) return `${token}-01`;
    return token;
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

function describeParameter(raw, context) {
  const parameter = context.resolveSchema(raw);
  const field = describeField(parameter.name, parameter.schema ?? {}, Boolean(parameter.required), context);
  field.description = sentence(parameter.description ?? context.resolveSchema(parameter.schema ?? {}).description ?? '');
  field.standard = standardField(parameter.name, parameter.in);
  if (field.standard && /cursor/.test(parameter.name)) field.rules = [];
  return {...field, in: parameter.in};
}

// Counts how often each component schema is referenced, so reused object
// types can get a page of their own.
function countReferences(specification) {
  const counts = new Map();
  for (const match of JSON.stringify(specification).matchAll(/"#\/components\/schemas\/([A-Za-z0-9_.-]+)"/g)) {
    counts.set(match[1], (counts.get(match[1]) ?? 0) + 1);
  }
  return counts;
}

export const typeRoute = `${referenceRoute}/types`;

function createContext(specification, resolveSchema) {
  const counts = countReferences(specification);
  const types = new Map();
  const context = {
    resolveSchema,
    trail: new Set(),
    user: null,
    types,
    shouldLink(name, shape) {
      const size = Object.keys(shape.properties ?? {}).length;
      return (counts.get(name) ?? 0) >= 2 && size >= 4;
    },
    link(name) {
      if (!types.has(name)) types.set(name, {name, slug: kebab(name), users: new Map()});
      if (context.user) types.get(name).users.set(context.user.key, context.user);
      return `${typeRoute}/${kebab(name)}`;
    },
  };
  return context;
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
  const context = createContext(specification, resolveSchema);
  const operations = [];

  for (const [path, item] of Object.entries(specification.paths ?? {})) {
    const shared = item.parameters ?? [];
    for (const method of methods) {
      const operation = item[method];
      if (!operation?.operationId) continue;
      context.user = {key: `op:${operation.operationId}`, kind: 'endpoint', id: kebab(operation.operationId), name: humanizeOperation(operation.operationId), method, path};
      const parameters = [...shared, ...(operation.parameters ?? [])].map((parameter) => describeParameter(parameter, context));
      const auth = operation['x-proctor-auth'] ?? 'public';
      const idempotency = operation['x-proctor-idempotency'] ?? 'none';

      let body = null;
      if (operation.requestBody) {
        const requestBody = resolveSchema(operation.requestBody);
        const [contentType, media] = Object.entries(requestBody.content ?? {})[0] ?? [];
        if (contentType) {
          const structured = contentType === 'application/json' || contentType === 'multipart/form-data';
          const described = structured ? describeObject(media.schema ?? {}, context, 'body') : {fields: [], closed: false, list: false};
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
        const described = contentType === 'application/json' && media?.schema ? describeObject(media.schema, context, 'response') : {fields: [], closed: false, list: false};
        response = {status: Number(status), label: statusLabels[status] ?? '', contentType: contentType ?? null, description: sentence(resolved.description ?? ''), ...described};
        if (contentType === 'application/json' && media?.schema) {
          const example = media.example ?? Object.values(media.examples ?? {})[0]?.value ?? exampleFor(media.schema, resolveSchema);
          responseExample = {status: Number(status), label: statusLabels[status] ?? '', ok: true, code: JSON.stringify(example, null, 2), fromContract: media.example !== undefined || media.examples !== undefined};
        }
      }

      // Redirects and protocol upgrades have no body; say what happens instead.
      let otherResponse = null;
      if (!successEntry) {
        const entry = responseEntries.find(([status]) => status === '101' || status.startsWith('3'));
        if (entry) {
          const [status, raw] = entry;
          const resolved = resolveSchema(raw);
          const headers = Object.entries(resolved.headers ?? {}).map(([name, header]) => {
            const value = resolveSchema(header);
            return {name, description: sentence(value.description ?? ''), type: typeLabel(resolveSchema(value.schema ?? {}), resolveSchema)};
          });
          otherResponse = {status: Number(status), label: statusLabels[status] ?? '', description: sentence(resolved.description ?? ''), headers};
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
        otherResponse,
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

  // A page for every linked type, following the links those pages add in turn.
  const typePages = [];
  const done = new Set();
  while (done.size < context.types.size) {
    for (const entry of [...context.types.values()]) {
      if (done.has(entry.name)) continue;
      done.add(entry.name);
      const raw = {$ref: `#/components/schemas/${entry.name}`};
      const schema = resolveSchema(raw);
      context.user = {key: `type:${entry.name}`, kind: 'type', name: entry.name, slug: entry.slug};
      context.trail = new Set([entry.name]);
      const described = describeObject(raw, context, 'type');
      context.trail = new Set();
      typePages.push({name: entry.name, slug: entry.slug, description: sentence(schema.description ?? ''), ...described, example: JSON.stringify(exampleFor(raw, resolveSchema), null, 2), entry});
    }
  }
  const types = typePages
    .map(({entry, ...page}) => ({...page, usedBy: [...entry.users.values()].filter((user) => user.key !== `type:${page.name}`).sort((a, b) => a.kind.localeCompare(b.kind) || a.name.localeCompare(b.name))}))
    .sort((a, b) => a.name.localeCompare(b.name));

  const tags = (specification.tags ?? []).map((tag) => ({
    name: tag.name,
    slug: kebab(tag.name),
    description: sentence(tag.description ?? ''),
    operations: operations.filter((operation) => operation.tag === tag.name).map(({id, name, method, path, summary}) => ({id, name, method, path, summary})),
  }));
  for (const operation of operations) {
    operation.more = (tags.find((tag) => tag.name === operation.tag)?.operations ?? []).filter((other) => other.id !== operation.id);
  }
  return {operations, tags, types, kernel: kernelCodes.map((code) => ({code, meaning: meaning(problems.get(code)?.description)}))};
}

function frontMatter(fields) {
  return `---\n${Object.entries(fields).map(([key, value]) => `${key}: ${JSON.stringify(value)}`).join('\n')}\n---\n`;
}

// Every file of the generated reference root, keyed by its path in it.
export function renderReferenceFiles({operations, tags, types = [], kernel}) {
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
  for (const type of types) {
    files.set(`types/${type.slug}.json`, `${JSON.stringify(type)}\n`);
    files.set(`types/${type.slug}.mdx`, `${frontMatter({
      id: type.slug,
      title: type.name,
      description: type.description || `The ${type.name} object and the endpoints that use it.`,
      sidebar_label: labelFromWords(type.slug.split('-')),
      hide_title: true,
      hide_table_of_contents: true,
      pagination_prev: null,
      pagination_next: null,
      custom_edit_url: null,
    })}
import SchemaPage from '@site/src/components/SchemaPage';
import data from './${type.slug}.json';

<SchemaPage data={data} />
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
  if (types.length > 0) {
    sidebar.push({
      type: 'category',
      label: 'Shared types',
      collapsed: true,
      items: types.map((type) => ({type: 'doc', id: `reference/types/${type.slug}`, label: labelFromWords(type.slug.split('-'))})),
    });
  }
  files.set('sidebar.ts', `// Generated from server/openapi.json. Do not edit by hand.\nimport type {SidebarsConfig} from '@docusaurus/plugin-content-docs';\n\nconst sidebar: SidebarsConfig[string] = ${JSON.stringify(sidebar, null, 2)};\n\nexport default sidebar;\n`);
  return files;
}
