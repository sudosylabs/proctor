// Error-code pages: one page per code in the server's error catalog.
//
// The catalog owns each code, its status and its one-line meaning; OpenAPI
// owns which endpoints declare it; server/i18n owns the title and detail the
// server sends. Authored guidance in docs/api/problem-guidance.yaml adds
// causes and fixes for a code. Everything else here is presentation.

import {readFile} from 'node:fs/promises';
import {dirname, resolve} from 'node:path';
import {fileURLToPath} from 'node:url';

import yaml from 'js-yaml';

import {kebab} from '../search/index.mjs';
import {labelFromWords} from '../api-reference/labels.mjs';

const moduleRoot = dirname(fileURLToPath(import.meta.url));
export const defaultRepoRoot = resolve(moduleRoot, '../../..');
export const problemsRoute = '/problems';
export const guidancePath = 'docs/api/problem-guidance.yaml';
export const exampleTypeBase = 'https://docs.example.edu/problems/';

const methods = new Set(['get', 'post', 'put', 'patch', 'delete', 'options', 'head']);

// The status phrase a reader sees beside the number.
const statusPhrases = {
  400: 'Bad Request',
  401: 'Unauthorized',
  403: 'Forbidden',
  404: 'Not Found',
  405: 'Method Not Allowed',
  409: 'Conflict',
  410: 'Gone',
  413: 'Content Too Large',
  429: 'Too Many Requests',
  500: 'Internal Server Error',
  501: 'Not Implemented',
  502: 'Bad Gateway',
  503: 'Service Unavailable',
};

// Mirrors problemPresentationName in server/httpapi/route/problem.go.
function presentationName(status) {
  switch (status) {
    case 400:
      return 'bad_request';
    case 401:
      return 'unauthorized';
    case 403:
      return 'forbidden';
    case 404:
      return 'not_found';
    case 409:
      return 'conflict';
    case 413:
      return 'content_too_large';
    case 429:
      return 'too_many_requests';
    case 503:
      return 'service_unavailable';
    default:
      return status >= 400 && status < 500 ? 'client_error' : 'internal';
  }
}

// Default guidance by the condition that decides the status. Authored guidance
// for a specific code replaces these lists.
const conditionGuidance = {
  invalid: {
    retry: 'No. Correct the request first.',
    causes: [
      'A required field is missing, or a value has the wrong type or format.',
      'A value is outside the range the endpoint accepts.',
      'An identifier refers to something of the wrong kind.',
    ],
    fix: [
      'Read `fields` in the response. When Proctor knows which input it rejected, it names it there.',
      'Correct those values against the endpoint’s request schema.',
      'Send the corrected request.',
    ],
  },
  denied: {
    retry: 'Not until the caller’s access changes.',
    causes: [
      'The signed-in account has no Role that allows this action in this scope.',
      'The action needs a stronger or more recent sign-in than the current Session has.',
      'An institution policy forbids the action.',
    ],
    fix: [
      'Check which Roles the account holds, and in which scope.',
      'If the endpoint needs a recent or strong Session, sign in again and repeat the action.',
      'Otherwise ask an administrator to grant the access, if it should be granted.',
    ],
  },
  disabled: {
    retry: 'Not until it is enabled again.',
    causes: ['The feature, account or resource has been turned off on this installation.'],
    fix: ['Ask an administrator whether it should be enabled. Nothing the caller sends will change this.'],
  },
  not_found: {
    retry: 'No.',
    causes: [
      'The identifier is mistyped, or belongs to another installation.',
      'The resource was deleted or archived.',
      'The resource exists, but the signed-in account is not allowed to see it.',
    ],
    fix: [
      'Check the identifier you sent.',
      'Check that you are signed in as an account that can see the resource.',
      'List or search for the resource again rather than reusing an identifier you stored earlier.',
    ],
  },
  conflict: {
    retry: 'Only after reading the current state.',
    causes: [
      'Someone else changed the resource after you read it.',
      'The resource is not in a state that allows this change.',
    ],
    fix: [
      'Read the resource again.',
      'Decide whether your change still makes sense against what you read.',
      'Send it again based on the current state.',
    ],
  },
  state: {
    retry: 'Only after the resource changes state.',
    causes: ['The resource is in a state that rejects this request.'],
    fix: [
      'Read the resource to see its current state.',
      'Take the action that moves it into a state that accepts this request, or stop if it never will.',
    ],
  },
  expired: {
    retry: 'No. Start again.',
    causes: ['The thing you referred to had a limited lifetime, and it has ended.'],
    fix: ['Create or request a new one, then repeat the action with it.'],
  },
  rate_limited: {
    retry: 'Yes, after waiting.',
    causes: [
      'Too many requests of this kind arrived in a short time.',
      'Behind a reverse proxy without trusted-proxy settings, many people can share one address and one limit.',
    ],
    fix: [
      'Wait before retrying. When the response has a `Retry-After` header, wait at least that many seconds.',
      'Back off further if the limit is hit again. Do not retry in a tight loop.',
    ],
  },
  internal: {
    retry: 'Possibly. Report it if it happens again.',
    causes: ['Proctor hit an error it did not expect. This is a fault in the server, not in your request.'],
    fix: [
      'Retry once. For a write, send the same `Idempotency-Key` so it cannot apply twice.',
      'If it fails again, report it with the `request_id` from the response. An operator can find the matching log entries by that ID.',
    ],
  },
  unavailable: {
    retry: 'Yes, after a short wait.',
    causes: [
      'A dependency, such as the database, is briefly unreachable.',
      'The node is starting, stopping or under maintenance.',
    ],
    fix: [
      'Retry after the `Retry-After` interval when the response includes one, or after a short backoff.',
      'For a write, keep the same `Idempotency-Key` so a retry cannot apply twice.',
      'If it lasts more than a few minutes, tell the operator of the installation.',
    ],
  },
};
conditionGuidance.busy = conditionGuidance.unavailable;
conditionGuidance.unauthenticated = {
  retry: 'After signing in or presenting the right credential.',
  causes: [
    'The request carried no credential, or not the kind this endpoint needs.',
    'The credential expired or was revoked.',
  ],
  fix: [
    'Check which credential the endpoint needs on its API reference page.',
    'Sign in again or obtain a new credential, then repeat the request.',
  ],
};

// When the condition word does not decide the status, the documented
// exception status does.
const statusKinds = {
  400: 'invalid',
  401: 'unauthenticated',
  403: 'denied',
  404: 'not_found',
  410: 'expired',
  429: 'rate_limited',
  500: 'internal',
  501: 'internal',
  502: 'unavailable',
  503: 'unavailable',
};

function conditionOf(code) {
  return code.slice(code.lastIndexOf('.') + 1);
}

// Which status condition word decides this code, following the catalog rule:
// the condition equals the word or ends with `_<word>`.
function conditionWord(condition, conditions) {
  for (const word of Object.keys(conditions)) {
    if (condition === word || condition.endsWith(`_${word}`)) return word;
  }
  return null;
}

// Codes group by their first segment, except Exam codes, which are numerous
// enough to group by their second: exam.attempt, exam.sitting and so on.
export function areaOf(code) {
  const parts = code.split('.');
  return parts[0] === 'exam' && parts.length > 2 ? `exam.${parts[1]}` : parts[0];
}

export function humanizeArea(area) {
  return labelFromWords(area.split(/[._]/));
}

// Inside a large area, codes with a further segment form a sub-group.
function subgroupOf(code, area) {
  const rest = code.slice(area.length + 1).split('.');
  return rest.length > 1 ? rest[0] : '';
}

function meaningOf(description) {
  const text = compact(description).replaceAll('`', '').split('; ')[0].replace(/\.$/, '');
  return text ? `${text}.` : '';
}

function compact(value) {
  return String(value ?? '').replace(/\s+/g, ' ').trim();
}

// Escapes MDX markup outside inline code spans, where a backslash would show.
export function mdxText(value) {
  return compact(value)
    .split(/(`[^`]*`)/)
    .map((part, index) =>
      index % 2 === 1 ? part : part.replace(/[\\{}<>]/g, (character) => `\\${character}`),
    )
    .join('');
}

function stripCode(value) {
  return compact(value).replaceAll('`', '');
}

function yamlString(value) {
  return JSON.stringify(stripCode(value));
}

export async function loadProblems({repoRoot = defaultRepoRoot} = {}) {
  const [catalogSource, openapiSource, i18nSource, guidanceSource] = await Promise.all([
    readFile(resolve(repoRoot, 'server/httpapi/route/error_catalog.yaml'), 'utf8'),
    readFile(resolve(repoRoot, 'server/openapi.json'), 'utf8'),
    readFile(resolve(repoRoot, 'server/i18n/en.json'), 'utf8'),
    readFile(resolve(repoRoot, guidancePath), 'utf8'),
  ]);
  const catalog = yaml.load(catalogSource);
  const openapi = JSON.parse(openapiSource);
  const messages = Object.fromEntries(JSON.parse(i18nSource).map((m) => [m.id, m.translation]));
  const guidance = yaml.load(guidanceSource) ?? {};

  const known = new Set(catalog.codes.map((entry) => entry.code));
  for (const code of Object.keys(guidance)) {
    if (!known.has(code)) throw new Error(`${guidancePath}: ${code} is not in the error catalog`);
  }

  const endpoints = new Map();
  for (const [path, item] of Object.entries(openapi.paths ?? {})) {
    for (const [method, operation] of Object.entries(item)) {
      if (!methods.has(method) || !operation?.operationId) continue;
      for (const code of operation['x-proctor-error-codes'] ?? []) {
        if (!endpoints.has(code)) endpoints.set(code, []);
        endpoints.get(code).push({
          method: method.toUpperCase(),
          path,
          title: operation.summary ?? operation.operationId,
          name: labelFromWords(kebab(operation.operationId).split('-')),
          href: `/api/reference/${kebab(operation.operationId)}`,
        });
      }
    }
  }

  return catalog.codes
    .map((entry) => {
      const condition = conditionOf(entry.code);
      const word = conditionWord(condition, catalog.conditions);
      const status = entry.status ?? (word ? catalog.conditions[word] : catalog.default_status);
      const kind = entry.status ? statusKinds[entry.status] ?? 'state' : word ?? 'state';
      const name = presentationName(status);
      const area = areaOf(entry.code);
      return {
        code: entry.code,
        area,
        areaLabel: humanizeArea(area),
        subgroup: subgroupOf(entry.code, area),
        meaning: meaningOf(entry.description),
        status,
        statusText: `${status} ${statusPhrases[status] ?? ''}`.trim(),
        title: messages[`problem.${name}.title`] ?? statusPhrases[status] ?? 'Error',
        detail: messages[`problem.${name}.detail`] ?? '',
        description: entry.description ?? '',
        retryAfter: entry.retry_after ?? null,
        websocket: Boolean(entry.websocket_fallback),
        anyRequest: Boolean(entry.transport || entry.problem),
        extension: entry.extension ?? null,
        defaults: conditionGuidance[kind] ?? conditionGuidance.state,
        guidance: guidance[entry.code] ?? null,
        endpoints: (endpoints.get(entry.code) ?? []).sort(
          (a, b) => a.path.localeCompare(b.path) || a.method.localeCompare(b.method),
        ),
      };
    })
    .sort((a, b) => a.code.localeCompare(b.code));
}

function list(items, ordered) {
  return items.map((item, index) => `${ordered ? `${index + 1}.` : '-'} ${mdxText(item)}`).join('\n');
}

function exampleResponse(problem) {
  const endpoint = problem.endpoints[0];
  const body = {
    type: `${exampleTypeBase}${problem.code}`,
    title: problem.title,
    status: problem.status,
    ...(problem.detail ? {detail: problem.detail} : {}),
    ...(endpoint ? {instance: endpoint.path.replace(/\{[^}]+\}/g, 'ybndrfg8ejkmcpqxot1uwisza3')} : {}),
    code: problem.code,
    request_id: 'KJ4RTMQZ7XWB6N3HDYF5VCPAGE',
  };
  return JSON.stringify(body, null, 2);
}

// The codes a reader most likely confuses with this one: those sharing the
// longest prefix first, then the rest of the area.
function relatedCodes(problem, problems) {
  const prefix = problem.code.slice(0, problem.code.lastIndexOf('.'));
  const siblings = problems.filter((other) => other.code !== problem.code && other.code.startsWith(`${prefix}.`));
  const area = problems.filter(
    (other) => other.code !== problem.code && other.area === problem.area && !siblings.includes(other),
  );
  return [...siblings, ...area].slice(0, 8);
}

function rowData(problem) {
  return {code: problem.code, prefix: `${problem.area}.`, status: problem.status, meaning: problem.meaning};
}

export function renderProblemPage(problem, problems) {
  const authored = problem.guidance ?? {};
  const causes = authored.causes ?? problem.defaults.causes;
  const fix = authored.fix ?? problem.defaults.fix;
  const retry = authored.retry ?? problem.defaults.retry;
  const related = relatedCodes(problem, problems);
  const head = {
    code: problem.code,
    prefix: `${problem.area}.`,
    status: problem.statusText,
    retry,
    retryAfter: problem.retryAfter,
    websocket: problem.websocket,
    extension: problem.extension,
  };
  const where = problem.endpoints.length > 0
    ? `<EndpointRows items={${JSON.stringify(problem.endpoints.map(({method, path, name, href}) => ({method, path, name, href})))}} />`
    : problem.anyRequest
      ? 'Any request can return this code. The server itself sends it, so no endpoint lists it.'
      : 'No endpoint in the API reference declares this code.';

  const sections = [
    `<ProblemHead {...${JSON.stringify(head)}} />`,
    '',
    mdxText(problem.description),
    '',
    ...(authored.means ? ['## What it means', '', mdxText(authored.means), ''] : []),
    '## Common causes',
    '',
    list(causes, false),
    '',
    '## How to fix it',
    '',
    list(fix, true),
    '',
    ...(authored.note ? [`<Callout tone="warn">${mdxText(authored.note)}</Callout>`, ''] : []),
    '## Where you’ll see it',
    '',
    where,
    '',
    '## Example response',
    '',
    `\`\`\`json title="${problem.statusText} · application/problem+json" showLineNumbers {${problem.endpoints.length > 0 ? 7 : 6}}`,
    exampleResponse(problem),
    '```',
    '',
    ...(related.length > 0
      ? ['## Related codes', '', `<CodeRows items={${JSON.stringify(related.map(rowData))}} />`, '']
      : []),
    authored.causes
      ? '<p className="problem-source">Status and endpoints come from the error catalog and the API contract. The explanation is written by hand.</p>'
      : '<p className="problem-source">Status and endpoints come from the error catalog and the API contract. Causes and fixes are the general advice for this kind of error.</p>',
  ];

  return `---
title: ${JSON.stringify(problem.code)}
sidebar_label: ${JSON.stringify(problem.code.slice(problem.area.length + 1))}
slug: /${problem.code}
description: ${yamlString(problem.description)}
hide_title: true
pagination_prev: null
pagination_next: null
custom_edit_url: https://github.com/sudosylabs/Proctor/edit/main/${guidancePath}
---

${sections.join('\n')}
`;
}

export function renderIndexPage(problems) {
  const areas = new Set(problems.map((problem) => problem.area)).size;
  const retryable = problems.filter((problem) => [429, 503].includes(problem.status)).length;
  return `---
title: Error codes
description: What each Proctor error code means, what causes it and how to resolve it.
slug: /
hide_table_of_contents: true
pagination_prev: null
pagination_next: null
custom_edit_url: null
---

import problems from './problems.json';

When a request fails, Proctor answers with [Problem Details](/api/guides/errors). Its \`code\` names exactly what went wrong and never changes, so branch on it rather than on the title or detail.

<ProblemIndex problems={problems} areas={${areas}} retryable={${retryable}} />
`;
}

export function indexData(problems) {
  return problems.map(({code, area, areaLabel, subgroup, status, meaning}) => ({
    code,
    area,
    areaLabel,
    subgroup,
    subgroupLabel: subgroup ? labelFromWords(subgroup.split('_')) : '',
    status,
    meaning,
  }));
}

export function renderSidebar(problems) {
  const areas = [];
  for (const problem of problems) {
    let area = areas.find((candidate) => candidate.key === problem.area);
    if (!area) {
      area = {key: problem.area, label: problem.areaLabel, items: []};
      areas.push(area);
    }
    area.items.push({type: 'doc', id: `${problem.area}/${problem.code}`, label: problem.code.slice(problem.area.length + 1), className: 'problem-code'});
  }
  areas.sort((a, b) => a.label.localeCompare(b.label));
  const sidebar = [
    {type: 'doc', id: 'index', label: 'All codes'},
    ...areas.map((area) => ({
      type: 'category',
      label: area.label,
      collapsed: true,
      className: 'problem-area',
      items: area.items,
    })),
  ];
  return `// Generated from the server error catalog. Do not edit by hand.\nimport type {SidebarsConfig} from '@docusaurus/plugin-content-docs';\n\nconst sidebar: SidebarsConfig[string] = ${JSON.stringify(sidebar, null, 2)};\n\nexport default sidebar;\n`;
}

// All files of the generated content root, keyed by path relative to it.
export function renderProblemFiles(problems) {
  const files = new Map();
  files.set('index.mdx', renderIndexPage(problems));
  files.set('problems.json', `${JSON.stringify(indexData(problems), null, 2)}\n`);
  files.set('sidebar.ts', renderSidebar(problems));
  for (const problem of problems) {
    files.set(`${problem.area}/${problem.code}.mdx`, renderProblemPage(problem, problems));
  }
  return files;
}
