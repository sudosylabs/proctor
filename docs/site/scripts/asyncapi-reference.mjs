// Renders the realtime reference pages from the reviewed server/asyncapi.json.
// The pages present the contract; they never add or rewrite protocol meaning.

export const referenceRoute = '/api/reference/realtime-protocol';
const handshakeRoute = '/api/reference/connect-web-socket';

export function messageSlug(key) {
  return key
    .replace(/[^a-zA-Z0-9]+/g, '-')
    .replace(/^-|-$/g, '')
    .toLowerCase();
}

function text(value) {
  return String(value ?? '')
    .replace(/\s+/g, ' ')
    .trim()
    .replace(/[{}<>]/g, (character) => `\\${character}`);
}

function cell(value) {
  return text(value).replace(/\|/g, '\\|');
}

function code(value) {
  return `\`${String(value).replace(/`/g, '')}\``;
}

function yamlString(value) {
  return JSON.stringify(text(value).replace(/\\([{}<>])/g, '$1'));
}

function component(document, reference) {
  const name = reference?.replace(/^#\/components\/schemas\//, '');
  const schema = document.components?.schemas?.[name];
  if (!schema) {
    throw new Error(`unresolved schema reference ${reference}`);
  }
  return schema;
}

export function describeType(document, schema) {
  if (schema.$ref) {
    const target = component(document, schema.$ref);
    return target.title ?? describeType(document, target);
  }
  if (schema.const !== undefined) {
    return code(JSON.stringify(schema.const));
  }
  if (schema.enum) {
    return `one of ${schema.enum.map((value) => code(value)).join(', ')}`;
  }
  if (schema.type === 'integer' && schema.minimum !== undefined) {
    return `integer, at least ${schema.minimum}`;
  }
  if (schema.type === 'string' && schema.format === 'date-time') {
    return 'RFC 3339 instant';
  }
  return schema.type ?? 'any JSON value';
}

export function memberTable(document, schema) {
  const resolved = schema.$ref ? component(document, schema.$ref) : schema;
  const required = new Set(resolved.required ?? []);
  const rows = Object.entries(resolved.properties ?? {}).map(([name, member]) => {
    // A shared scalar such as an identifier is named by its type column; a
    // shared object keeps its description.
    const target = member.$ref ? component(document, member.$ref) : member;
    const description = member.$ref && target.type !== 'object' ? '' : target.description ?? '';
    return `| ${code(name)} | ${cell(describeType(document, member))} | ${required.has(name) ? 'Yes' : 'No'} | ${cell(description)} |`;
  });
  if (rows.length === 0) {
    return '';
  }
  return ['| Member | Type | Required | Description |', '| --- | --- | --- | --- |', ...rows].join('\n');
}

function narrowing(message) {
  const allOf = message.payload?.allOf;
  return Array.isArray(allOf) && allOf.length === 2 ? allOf[1] : undefined;
}

function isEvent(message) {
  return (message.tags ?? []).some((tag) => tag.name === 'event');
}

function group(message) {
  if (isEvent(message)) {
    return (message.tags ?? []).find((tag) => tag.name !== 'event')?.name ?? 'Events';
  }
  return 'Connection';
}

function delivery(message) {
  const narrowed = narrowing(message);
  if (!narrowed) {
    return [];
  }
  const lines = [];
  if ((narrowed.required ?? []).includes('user_id')) {
    lines.push('Addressed to one User: only that User\'s connections receive it.');
  }
  const action = narrowed.properties?.action?.const;
  if (action) {
    lines.push(`Requires a subscription to ${code(action)} on the event's ${code('resource')}.`);
  }
  return lines;
}

function frontmatter(fields) {
  return [
    '---',
    ...Object.entries(fields).map(([key, value]) => `${key}: ${typeof value === 'number' ? value : yamlString(value)}`),
    '---',
    '',
    '{/* Generated from server/asyncapi.json by docs/site/scripts/generate-asyncapi-reference.mjs. Do not edit by hand. */}',
    '',
  ].join('\n');
}

export function renderMessagePage(document, key) {
  const message = document.components.messages[key];
  const narrowed = narrowing(message);
  const data = narrowed?.properties?.data;
  const sections = [
    frontmatter({
      title: message.title ?? key,
      description: message.summary ?? message.title ?? key,
      audience: 'api-consumer',
      maturity: 'preview',
    }),
    `# ${text(message.title ?? key)}`,
    '',
    // An event's description ends with its audience, which Delivery shows.
    text(isEvent(message) ? message.summary : message.description ?? message.summary),
    '',
  ];

  if (isEvent(message)) {
    sections.push('## Delivery', '', `The frame's ${code('event')} member is ${code(message.name)}.`);
    for (const line of delivery(message)) {
      sections.push('', line);
    }
    sections.push(
      '',
      `Every event uses the [event frame](${referenceRoute}/#event-frame).`,
      '',
      '## Data',
      '',
      data ? memberTable(document, data) : 'This event has no data. Refetch the resource it names.',
      '',
    );
  } else {
    // A command or reply narrows a generic frame; show the data it carries,
    // or the whole frame when the message is the frame itself.
    const shape = data ?? (narrowed ? undefined : message.payload);
    if (shape) {
      sections.push(data ? '## Data' : '## Members', '', memberTable(document, shape), '');
    }
  }

  for (const example of message.examples ?? []) {
    sections.push(
      `## Example${message.examples.length > 1 ? `: ${text(example.name)}` : ''}`,
      '',
      '```json',
      JSON.stringify(example.payload, null, 2),
      '```',
      '',
    );
  }
  return sections.join('\n');
}

export function renderIndexPage(document) {
  const channel = document.channels.websocket;
  const messages = document.components.messages;
  const sections = [
    frontmatter({
      title: 'Realtime Protocol',
      description: 'Every frame of the WebSocket protocol: the event frame, commands and replies, close codes, and each event.',
      audience: 'api-consumer',
      maturity: 'preview',
    }),
    '# Realtime Protocol',
    '',
    `The connection is ${code(channel.address)}. [Connect WebSocket](${handshakeRoute}) describes the HTTP upgrade, its authentication, and the ${code('connection_id')} and ${code('sequence_number')} parameters that resume a connection.`,
    '',
    text(channel.description),
    '',
    `The contract is also available as [asyncapi.json](pathname:///asyncapi/asyncapi.json), an AsyncAPI ${document.asyncapi} document.`,
    '',
    '## Event frame',
    '',
    memberTable(document, {$ref: '#/components/schemas/event_frame'}),
    '',
    '## Commands',
    '',
    memberTable(document, {$ref: '#/components/schemas/command_request'}),
    '',
    '| Command | Replies | Error codes |',
    '| --- | --- | --- |',
  ];
  for (const operation of Object.values(document.operations)) {
    if (operation.action !== 'receive') {
      continue;
    }
    const [command] = operation.messages.map((item) => item.$ref.split('/').pop());
    const replies = (operation.reply?.messages ?? []).map((item) => item.$ref.split('/').pop());
    sections.push(
      `| [${code(messages[command].name)}](${referenceRoute}/${messageSlug(command)}) | ${replies
        .map((reply) => `[${code(reply)}](${referenceRoute}/${messageSlug(reply)})`)
        .join(', ')} | ${(operation['x-proctor-error-codes'] ?? []).map((value) => code(value)).join(', ')} |`,
    );
  }
  sections.push('', '## Close codes', '', '| Code | Name | Meaning |', '| --- | --- | --- |');
  for (const entry of channel['x-proctor-close-codes'] ?? []) {
    sections.push(`| ${entry.code} | ${code(entry.name)} | ${cell(entry.description)} |`);
  }
  sections.push('', '## Messages', '');
  for (const [title, keys] of groupedMessages(document)) {
    sections.push(`### ${text(title)}`, '', '| Message | Summary |', '| --- | --- |');
    for (const key of keys) {
      sections.push(`| [${text(messages[key].title ?? key)}](${referenceRoute}/${messageSlug(key)}) | ${cell(messages[key].summary)} |`);
    }
    sections.push('');
  }
  return sections.join('\n');
}

export function groupedMessages(document) {
  const groups = new Map();
  for (const ref of Object.values(document.channels.websocket.messages)) {
    const key = ref.$ref.split('/').pop();
    const title = group(document.components.messages[key]);
    groups.set(title, [...(groups.get(title) ?? []), key]);
  }
  // The connection's own frames come first; event areas follow by name.
  return [...groups.entries()].sort(([left], [right]) =>
    left === 'Connection' ? -1 : right === 'Connection' ? 1 : left.localeCompare(right),
  );
}

export function renderSidebar(document) {
  const items = [{type: 'doc', id: 'reference/realtime-protocol/index', label: 'Overview'}];
  for (const [title, keys] of groupedMessages(document)) {
    items.push({
      type: 'category',
      label: title,
      collapsed: true,
      items: keys.map((key) => ({
        type: 'doc',
        id: `reference/realtime-protocol/${messageSlug(key)}`,
        label: document.components.messages[key].title ?? key,
      })),
    });
  }
  return (
    '// Generated from server/asyncapi.json. Do not edit by hand.\n' +
    "import type {SidebarsConfig} from '@docusaurus/plugin-content-docs';\n\n" +
    `const sidebar: SidebarsConfig[string] = ${JSON.stringify(items, null, 2)};\n\nexport default sidebar;\n`
  );
}

// renderReference returns every generated file, keyed by its name inside the
// realtime reference directory.
export function renderReference(document) {
  if (document?.asyncapi !== '3.0.0' || !document.channels?.websocket) {
    throw new Error('server/asyncapi.json is not the Proctor AsyncAPI 3.0.0 document');
  }
  const files = new Map([['index.mdx', renderIndexPage(document)]]);
  for (const ref of Object.values(document.channels.websocket.messages)) {
    const key = ref.$ref.split('/').pop();
    const name = `${messageSlug(key)}.mdx`;
    if (files.has(name)) {
      throw new Error(`two messages render to ${name}`);
    }
    files.set(name, renderMessagePage(document, key));
  }
  files.set('sidebar.ts', renderSidebar(document));
  return files;
}
