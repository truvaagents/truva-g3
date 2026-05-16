import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';

const config: Config = {
  title: 'TruvaG3',
  tagline: 'True. Dynamic discovery. Decentralized. Observable.',
  favicon: 'img/favicon.ico',

  future: {
    v4: true,
  },

  // Until the custom domain is live, the site deploys to
  // https://truvaagents.github.io/truva-g3/. Flip baseUrl to '/' and url to
  // 'https://truvag3.dev' when the custom domain lands.
  url: 'https://truvaagents.github.io',
  baseUrl: '/truva-g3/',

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
            'DOCUMENTATION_STRATEGY.md',
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
      // CSS on `.navbar__title`. See website/src/css/custom.css.
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
