// Turning identifiers into labels: sentence case, with acronyms and product
// spellings kept. Shared by the endpoint and error-code generators.

export const fixedWords = {
  api: 'API', csv: 'CSV', csrf: 'CSRF', dpop: 'DPoP', http: 'HTTP', id: 'ID', ids: 'IDs',
  json: 'JSON', mfa: 'MFA', oauth: 'OAuth', oidc: 'OIDC', pdf: 'PDF', saml: 'SAML', sso: 'SSO',
  totp: 'TOTP', url: 'URL', uri: 'URI', vfs: 'VFS', websocket: 'WebSocket', zip: 'ZIP',
};

export function labelFromWords(words) {
  const text = words
    .filter(Boolean)
    .map((word) => fixedWords[word.toLowerCase()] ?? word.toLowerCase())
    .join(' ')
    .replace(/\bweb socket\b/g, 'WebSocket');
  return text.charAt(0).toUpperCase() + text.slice(1);
}
