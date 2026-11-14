import {inflateSync} from 'node:zlib';

export function insertApiContractPanel(source, name = 'generated endpoint') {
  if (source.includes('<ApiContractPanel />')) {
    return source;
  }

  if (!/\n<ParamsDetails\b/.test(source)) {
    throw new Error(`${name}: could not locate the generated request-details seam`);
  }

  return source.replace(
    /\n<ParamsDetails\b/,
    '\n<ApiContractPanel />\n\n<ParamsDetails',
  );
}

// The generator stores each operation in the api front matter as deflated,
// base64-encoded JSON. Uncompressed, it was hundreds of kilobytes of object
// literal per page, and bundling 300 such pages exhausted the CI runner.
export function readGeneratedApi(source, name = 'generated endpoint') {
  const match = source.match(/^api:\s*(\S+)\s*$/m);
  if (!match) {
    throw new Error(`${name}: missing api frontmatter`);
  }

  try {
    const value = match[1];
    const json = value.startsWith('{')
      ? value
      : inflateSync(Buffer.from(value, 'base64')).toString('utf8');
    return JSON.parse(json);
  } catch (error) {
    throw new Error(`${name}: invalid api frontmatter: ${error.message}`);
  }
}

export const contractExtensions = [
  'x-proctor-auth',
  'x-proctor-error-codes',
  'x-proctor-idempotency',
];

// ApiContractPanel reads these few fields, so they stay readable in their
// own front matter entry instead of the panel decompressing the operation.
export function insertProctorContract(source, name = 'generated endpoint') {
  const api = readGeneratedApi(source, name);
  const contract = Object.fromEntries(
    contractExtensions.map((extension) => [extension, api[extension]]),
  );
  const line = `proctor_contract: ${JSON.stringify(contract)}`;

  if (/^proctor_contract:/m.test(source)) {
    return source.replace(/^proctor_contract:.*$/m, line);
  }
  return source.replace(/^(api:.*)$/m, `$1\n${line}`);
}
