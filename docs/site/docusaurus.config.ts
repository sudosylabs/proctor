import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';
import {guides, referenceLinks, sourceRepository} from './navigation.mjs';
import codeTheme from './src/code-theme';

const baseUrl = (process.env.BASE_URL ?? '/').replace(/\/?$/, '/');

const config: Config = {
  title: 'Proctor Documentation',
  tagline: 'Run examinations with the rules in view',
  headTags: [
    {
      tagName: 'link',
      attributes: {
        rel: 'icon',
        type: 'image/png',
        sizes: '32x32',
        href: `${baseUrl}img/brand/proctor-favicon-light-32.png`,
      },
    },
    ...(['light', 'dark'] as const).map((scheme) => ({
      tagName: 'link',
      attributes: {
        rel: 'icon',
        type: 'image/svg+xml',
        href: `${baseUrl}img/brand/proctor-favicon-${scheme}.svg`,
        media: `(prefers-color-scheme: ${scheme})`,
      },
    })),
  ],

  // Keep authored and generated content on Docusaurus' forward-compatible MDX
  // parser. The API plugin has its own content root so every file is compiled
  // exactly once.
  future: {
    v4: true,
  },

  // Publication is intentionally deferred. The placeholder prevents a local
  // build from inventing a production hostname before that decision is made.
  url: process.env.DOCS_SITE_URL ?? 'https://docs.proctor.invalid',
  baseUrl,
  staticDirectories: ['static', '../public/static'],
  trailingSlash: false,

  organizationName: 'sudosylabs',
  projectName: 'Proctor',

  onBrokenLinks: 'throw',
  markdown: {
    hooks: {
      onBrokenMarkdownLinks: 'throw',
      onBrokenMarkdownImages: 'throw',
    },
  },

  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  presets: [
    [
      'classic',
      {
        docs: {
          path: '../public',
          routeBasePath: '/',
          sidebarPath: './sidebars.ts',
          editUrl: ({docPath}) =>
            `https://github.com/sudosylabs/Proctor/edit/main/docs/public/${docPath}`,
          breadcrumbs: true,
        },
        blog: false,
        theme: {
          customCss: [
            './src/css/tokens.css',
            './src/css/custom.css',
            './src/css/api.css',
          ],
        },
      } satisfies Preset.Options,
    ],
  ],

  plugins: [
    [
      '@docusaurus/plugin-content-docs',
      {
        id: 'api',
        path: '../api',
        routeBasePath: '/api',
        sidebarPath: './sidebars.api.ts',
        editUrl: ({docPath}: {docPath: string}) =>
          `https://github.com/sudosylabs/Proctor/edit/main/docs/api/${docPath}`,
        breadcrumbs: true,
      },
    ],
    [
      '@docusaurus/plugin-content-docs',
      {
        // Generated from the server error catalog by npm run generate:problems.
        id: 'problems',
        path: '../problems',
        routeBasePath: '/problems',
        sidebarPath: './sidebars.problems.ts',
        breadcrumbs: true,
      },
    ],
  ],

  themeConfig: {
    metadata: [
      {
        name: 'keywords',
        content: 'Proctor, examination, self-hosted, documentation, API',
      },
    ],
    colorMode: {
      defaultMode: 'light',
      respectPrefersColorScheme: true,
    },
    navbar: {
      hideOnScroll: false,
      items: [
        {
          type: 'dropdown',
          label: 'Guides',
          position: 'left',
          items: guides.map(({label, sidebarId}) => ({
            type: 'docSidebar',
            label,
            sidebarId,
          })),
        },
        {to: '/api/', label: 'API', position: 'left', activeBasePath: '/api'},
        {
          type: 'dropdown',
          label: 'Reference',
          position: 'left',
          items: referenceLinks,
        },
        {to: '/problems/', label: 'Error codes', position: 'left', activeBasePath: '/problems'},
        {type: 'search', position: 'right'},
        {
          type: 'html',
          position: 'right',
          value: `<a class="header-source-link" href="${sourceRepository}" aria-label="Source code on GitHub" rel="noopener noreferrer" target="_blank"></a>`,
        },
      ],
    },
    prism: {
      theme: codeTheme,
      darkTheme: codeTheme,
      additionalLanguages: ['bash', 'json', 'go', 'python', 'http'],
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
