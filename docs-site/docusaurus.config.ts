import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';

const config: Config = {
  title: 'TruvaG3',
  tagline: 'True. Dynamic Discovery. Decentralized. Observable.',
  favicon: 'img/favicon.svg',

  future: {
    v4: true,
  },

  // Docs surface is deployed at docs.truvag3.dev via Cloudflare Pages
  // (truva-g3-docs project, root directory: docs-site/). The marketing-free
  // root site lives at truvag3.dev (separate www/ folder, separate CF Pages
  // project). The pages.dev preview URL during initial CF setup will be
  // https://truva-g3-docs.pages.dev/ — same baseUrl works there too.
  url: 'https://docs.truvag3.dev',
  baseUrl: '/',

  organizationName: 'truvaagents',
  projectName: 'truva-g3',

  onBrokenLinks: 'throw',
  markdown: {
    hooks: {
      onBrokenMarkdownLinks: 'throw',
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
          path: '../docs',
          routeBasePath: 'docs',
          sidebarPath: './sidebars.ts',
          exclude: [
            'DOCUMENTATION_STRATEGY_NEW.md',
            'PAPER_DRAFT_*.md',
            'PAPER_NOVELTY_ASSESSMENT.md',
            'REDIS_DEPENDENCY_ANALYSIS.md',
            'overview/ARCHITECTURE.md',
          ],
          editUrl: 'https://github.com/truvaagents/truva-g3/edit/main/docs/',
          showLastUpdateAuthor: true,
          showLastUpdateTime: true,
        },
        blog: false,
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
    ],
  ],

  themeConfig: {
    image: 'img/docusaurus-social-card.jpg',
    colorMode: {
      defaultMode: 'light',
      respectPrefersColorScheme: true,
    },
    navbar: {
      title: 'TruvaG3',
      // No logo image yet — the two-tone "TruvaG3" brand mark is painted via
      // CSS on `.navbar__title`. See docs-site/src/css/custom.css.
      items: [
        {
          type: 'docSidebar',
          sidebarId: 'tutorialSidebar',
          position: 'left',
          label: 'Docs',
        },
        {
          href: 'https://github.com/truvaagents/truva-g3',
          label: 'GitHub',
          position: 'right',
        },
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: 'Docs',
          items: [
            {
              label: 'Introduction',
              to: '/docs/intro',
            },
            {
              label: 'Getting started',
              to: '/docs/getting-started',
            },
            {
              label: 'Framework Features',
              to: '/docs/overview/FRAMEWORK_FEATURES_GUIDE',
            },
          ],
        },
        {
          title: 'Community',
          items: [
            {
              label: 'GitHub',
              href: 'https://github.com/truvaagents/truva-g3',
            },
          ],
        },
      ],
      copyright: `Copyright © ${new Date().getFullYear()} TruvaG3. Built with Docusaurus.`,
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
