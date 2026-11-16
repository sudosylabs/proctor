import type {PrismTheme} from 'prism-react-renderer';

// One theme for both color modes: every color is a design token, so the
// generated light and dark values in tokens.css decide what readers see.
const token = (name: string) => `var(--proctor-${name})`;

const codeTheme: PrismTheme = {
  plain: {
    color: token('code-block-ink'),
    backgroundColor: token('code-block'),
  },
  styles: [
    {types: ['comment', 'prolog', 'doctype', 'cdata'], style: {color: token('syntax-comment')}},
    {types: ['punctuation', 'operator'], style: {color: token('syntax-punctuation')}},
    {types: ['property', 'tag', 'keyword', 'selector', 'important', 'atrule'], style: {color: token('syntax-key')}},
    {types: ['string', 'char', 'attr-value', 'inserted', 'regex', 'url'], style: {color: token('syntax-string')}},
    {types: ['number', 'boolean', 'constant', 'symbol', 'parameter'], style: {color: token('syntax-number')}},
    {types: ['variable', 'environment'], style: {color: token('syntax-variable')}},
    {types: ['function', 'class-name', 'builtin'], style: {color: token('syntax-key')}},
    {types: ['deleted'], style: {color: token('danger')}},
    {types: ['italic'], style: {fontStyle: 'italic'}},
    {types: ['bold'], style: {fontWeight: '600'}},
  ],
};

export default codeTheme;
