// Documentation diagrams are authored as small YAML layouts under
// docs/public/diagrams/ and drawn by the Diagram component in the site's own
// type and colour tokens. This module owns their grammar: it parses a source,
// rejects anything the renderer cannot draw, and returns the normalized model
// the component reads. Nothing here positions pixels.

import {readFile, readdir} from 'node:fs/promises';
import {dirname, resolve} from 'node:path';
import {fileURLToPath} from 'node:url';

import yaml from 'js-yaml';

const moduleRoot = dirname(fileURLToPath(import.meta.url));
export const defaultRepoRoot = resolve(moduleRoot, '../../..');
export const diagramRoot = 'docs/public/diagrams';

// A row with more items than this cannot keep its labels readable at the
// documentation column width; split it into two rows instead.
export const maximumRowItems = 4;

const nodeKinds = ['card', 'group', 'branch'];
const ruleKinds = new Set(['none', 'solid', 'dashed', 'bracket']);
const gapKinds = new Set(['normal', 'wide']);
// An arrow label sits in the gap between two row items. A normal gap holds one
// short word; a wide gap holds two short lines.
export const labelLimits = {normal: 9, wide: 26};

function isObject(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function text(value, where, failures, {optional = false} = {}) {
  if (value === undefined && optional) {
    return undefined;
  }
  if (typeof value !== 'string' || value.trim() === '') {
    failures.push(`${where} must be nonempty text`);
    return undefined;
  }
  if (/[<>]/.test(value)) {
    failures.push(`${where} must be plain text; markup is not allowed`);
  }
  if (/https?:\/\//i.test(value)) {
    failures.push(`${where} must not contain a URL`);
  }
  const marks = value.match(/\*\*/g)?.length ?? 0;
  const ticks = value.match(/`/g)?.length ?? 0;
  if (marks % 2 !== 0 || ticks % 2 !== 0) {
    failures.push(`${where} has an unclosed ** or \` mark`);
  }
  return value.trim();
}

function flag(value, where, failures) {
  if (value === undefined) {
    return false;
  }
  if (typeof value !== 'boolean') {
    failures.push(`${where} must be true or false`);
    return false;
  }
  return value;
}

function onlyKeys(object, allowed, where, failures) {
  for (const key of Object.keys(object)) {
    if (!allowed.includes(key)) {
      failures.push(`${where} has unknown field ${key}`);
    }
  }
}

// An arrow is either a label, `none`, or an object with a label, an optional
// step number, and the accent or dashed treatment.
function arrow(value, where, failures) {
  if (value === undefined) {
    return {};
  }
  if (value === 'none') {
    return null;
  }
  if (typeof value === 'string') {
    return {label: text(value, where, failures)};
  }
  if (!isObject(value)) {
    failures.push(`${where} must be a label, none, or an arrow object`);
    return {};
  }
  onlyKeys(value, ['label', 'step', 'accent', 'dashed'], where, failures);
  const result = {
    label: text(value.label, `${where}.label`, failures, {optional: true}),
    accent: flag(value.accent, `${where}.accent`, failures),
    dashed: flag(value.dashed, `${where}.dashed`, failures),
  };
  if (value.step !== undefined) {
    if (!Number.isSafeInteger(value.step) || value.step < 1 || value.step > 9) {
      failures.push(`${where}.step must be a whole number from 1 to 9`);
    } else {
      result.step = value.step;
    }
  }
  return result;
}

function lines(value, where, failures) {
  if (value === undefined) {
    return undefined;
  }
  if (!Array.isArray(value) || value.length === 0) {
    failures.push(`${where} must be a nonempty list`);
    return undefined;
  }
  return value.map((line, index) => text(line, `${where}[${index + 1}]`, failures));
}

function card(value, where, failures) {
  onlyKeys(
    value,
    ['card', 'text', 'items', 'numbered', 'accent', 'dashed', 'quiet', 'via', 'span'],
    where,
    failures,
  );
  return {
    type: 'card',
    title: text(value.card, `${where}.card`, failures),
    text: text(value.text, `${where}.text`, failures, {optional: true}),
    items: lines(value.items, `${where}.items`, failures),
    numbered: flag(value.numbered, `${where}.numbered`, failures),
    accent: flag(value.accent, `${where}.accent`, failures),
    dashed: flag(value.dashed, `${where}.dashed`, failures),
    quiet: flag(value.quiet, `${where}.quiet`, failures),
  };
}

function group(value, where, failures) {
  onlyKeys(
    value,
    ['group', 'text', 'items', 'columns', 'accent', 'dashed', 'via', 'span'],
    where,
    failures,
  );
  const columns = value.columns ?? 1;
  if (![1, 2, 3].includes(columns)) {
    failures.push(`${where}.columns must be 1, 2, or 3`);
  }
  if (!Array.isArray(value.items) || value.items.length === 0) {
    failures.push(`${where}.items must list the cards inside the group`);
  }
  return {
    type: 'group',
    title: text(value.group, `${where}.group`, failures),
    text: text(value.text, `${where}.text`, failures, {optional: true}),
    columns,
    accent: flag(value.accent, `${where}.accent`, failures),
    dashed: flag(value.dashed, `${where}.dashed`, failures),
    items: (Array.isArray(value.items) ? value.items : []).map((item, index) => {
      const place = `${where}.items[${index + 1}]`;
      if (!isObject(item) || typeof item.card !== 'string') {
        failures.push(`${place} must be a card`);
        return {type: 'card', title: ''};
      }
      for (const key of ['via', 'span']) {
        if (key in item) {
          failures.push(`${place} cannot use ${key} inside a group`);
        }
      }
      return card(item, place, failures);
    }),
  };
}

function branch(value, where, failures) {
  onlyKeys(value, ['branch', 'items', 'footer', 'via', 'span'], where, failures);
  const title = value.branch === true ? undefined : text(value.branch, `${where}.branch`, failures);
  if (!Array.isArray(value.items) || value.items.length < 2) {
    failures.push(`${where}.items must list at least two outcomes`);
  }
  return {
    type: 'branch',
    title,
    footer: text(value.footer, `${where}.footer`, failures, {optional: true}),
    items: (Array.isArray(value.items) ? value.items : []).map((item, index) => {
      const place = `${where}.items[${index + 1}]`;
      if (!isObject(item)) {
        failures.push(`${place} must be an outcome with a title`);
        return {title: ''};
      }
      onlyKeys(item, ['title', 'text', 'accent'], place, failures);
      return {
        title: text(item.title, `${place}.title`, failures),
        text: text(item.text, `${place}.text`, failures, {optional: true}),
        accent: flag(item.accent, `${place}.accent`, failures),
      };
    }),
  };
}

function node(value, where, failures) {
  if (!isObject(value)) {
    failures.push(`${where} must be a card, group, or branch`);
    return null;
  }
  const kinds = nodeKinds.filter((kind) => kind in value);
  if (kinds.length !== 1) {
    failures.push(`${where} must name exactly one of card, group, or branch`);
    return null;
  }
  const parsed =
    kinds[0] === 'card'
      ? card(value, where, failures)
      : kinds[0] === 'group'
        ? group(value, where, failures)
        : branch(value, where, failures);
  parsed.via = arrow(value.via, `${where}.via`, failures);
  parsed.span = value.span ?? 1;
  if (![1, 2, 3].includes(parsed.span)) {
    failures.push(`${where}.span must be 1, 2, or 3`);
  }
  return parsed;
}

function connectors(value, previous, items, where, failures) {
  if (value === undefined) {
    return undefined;
  }
  if (!previous) {
    failures.push(`${where} needs a row above it to connect from`);
    return undefined;
  }
  if (!Array.isArray(value) || value.length === 0) {
    failures.push(`${where} must list at least one connector`);
    return undefined;
  }
  return value.map((connector, index) => {
    const place = `${where}[${index + 1}]`;
    if (!isObject(connector)) {
      failures.push(`${place} must be a connector object`);
      return null;
    }
    onlyKeys(connector, ['from', 'to', 'label', 'step', 'accent', 'dashed'], place, failures);
    const {from, to} = connector;
    if (!Number.isSafeInteger(from) || from < 1 || from > previous.items.length) {
      failures.push(`${place}.from must name an item of the row above, 1 to ${previous.items.length}`);
    }
    if (!Number.isSafeInteger(to) || to < 1 || to > items.length) {
      failures.push(`${place}.to must name an item of this row, 1 to ${items.length}`);
    }
    const line = arrow(
      {label: connector.label, step: connector.step, accent: connector.accent, dashed: connector.dashed},
      place,
      failures,
    );
    return {...line, from, to};
  });
}

function row(value, previous, where, failures) {
  onlyKeys(value, ['row', 'from', 'gap', 'links', 'stack'], where, failures);
  if (!Array.isArray(value.row) || value.row.length === 0) {
    failures.push(`${where}.row must list at least one item`);
    return null;
  }
  const stack = flag(value.stack, `${where}.stack`, failures);
  if (!stack && value.row.length > maximumRowItems) {
    failures.push(
      `${where}.row has ${value.row.length} items; split it so no row holds more than ${maximumRowItems}`,
    );
  }
  const gap = value.gap ?? 'normal';
  if (!gapKinds.has(gap)) {
    failures.push(`${where}.gap must be normal or wide`);
  }
  if (value.links !== undefined && value.links !== 'none') {
    failures.push(`${where}.links may only be none`);
  }
  const items = value.row.map((item, index) => node(item, `${where}.row[${index + 1}]`, failures)).filter(Boolean);
  items.forEach((item, index) => {
    const label = item.via?.label?.replace(/\*\*|`/g, '');
    const limit = labelLimits[gap] ?? labelLimits.normal;
    if (index > 0 && value.links !== 'none' && label && label.length > limit) {
      failures.push(
        `${where}.row[${index + 1}].via label "${label}" is longer than ${limit} characters; shorten it or set gap: wide`,
      );
    }
  });
  if (value.row[0]?.via !== undefined) {
    failures.push(`${where}.row[1].via has no item to its left`);
  }
  items.forEach((item, index) => {
    // The first item has nothing to its left, and a row without links only
    // places its items side by side.
    if (index === 0 || value.links === 'none') {
      item.via = null;
    }
  });
  return {
    type: 'row',
    items,
    gap,
    stack,
    from: connectors(value.from, previous, items, `${where}.from`, failures),
  };
}

function note(value, where, failures) {
  onlyKeys(value, ['note', 'rule'], where, failures);
  const rule = value.rule ?? 'none';
  if (!ruleKinds.has(rule)) {
    failures.push(`${where}.rule must be none, solid, dashed, or bracket`);
  }
  return {type: 'note', text: text(value.note, `${where}.note`, failures), rule};
}

export function parseDiagram(source, name = 'diagram') {
  const failures = [];
  let document;
  try {
    document = yaml.load(source, {schema: yaml.CORE_SCHEMA});
  } catch (error) {
    return {diagram: null, failures: [`${name}: ${error.message}`]};
  }
  if (!isObject(document)) {
    return {diagram: null, failures: [`${name}: a diagram must be an object with blocks`]};
  }
  onlyKeys(document, ['blocks'], name, failures);
  if (!Array.isArray(document.blocks) || document.blocks.length === 0) {
    failures.push(`${name}: blocks must list at least one row or note`);
    return {diagram: null, failures};
  }
  const blocks = [];
  let previous = null;
  document.blocks.forEach((block, index) => {
    const where = `${name}: blocks[${index + 1}]`;
    if (isObject(block) && 'row' in block) {
      const parsed = row(block, previous, where, failures);
      if (parsed) {
        blocks.push(parsed);
        previous = parsed;
      }
    } else if (isObject(block) && 'note' in block) {
      blocks.push(note(block, where, failures));
      previous = null;
    } else {
      failures.push(`${where} must be a row or a note`);
    }
  });
  return {diagram: {blocks}, failures};
}

export async function loadDiagrams({repoRoot = defaultRepoRoot} = {}) {
  const root = resolve(repoRoot, diagramRoot);
  let names;
  try {
    names = (await readdir(root)).filter((name) => name.endsWith('.yaml')).sort();
  } catch (error) {
    if (error.code === 'ENOENT') {
      return {diagrams: {}, failures: []};
    }
    throw error;
  }
  const diagrams = {};
  const failures = [];
  for (const name of names) {
    const source = await readFile(resolve(root, name), 'utf8');
    const result = parseDiagram(source, `${diagramRoot}/${name}`);
    failures.push(...result.failures);
    if (result.diagram) {
      diagrams[name.slice(0, -'.yaml'.length)] = result.diagram;
    }
  }
  return {diagrams, failures};
}
