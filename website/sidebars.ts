import type {SidebarsConfig} from '@docusaurus/plugin-content-docs';

// This runs in Node.js - Don't use client-side code here (browser APIs, JSX...)

/**
 * Creating a sidebar enables you to:
 - create an ordered group of docs
 - render a sidebar for each doc of that group
 - provide next/previous navigation

 The sidebars can be generated from the filesystem, or explicitly defined here.

 Create as many sidebars as you want.
 */
const sidebars: SidebarsConfig = {
  docs: [
    {
      type: 'category',
      label: 'Getting Started',
      items: [
        'getting-started/intro',
        'getting-started/installation',
        'getting-started/quick-start',
        'getting-started/project-layout',
      ],
    },
    {
      type: 'category',
      label: 'Concepts',
      items: [
        'concepts/environments-regions',
        'concepts/resources-overview',
        'concepts/bootstrap-escrow',
        'concepts/provision-vs-deploy',
      ],
    },
    {
      type: 'category',
      label: 'Resource Reference',
      items: [
        'resources/network',
        'resources/compute',
        'resources/dns',
        'resources/database',
        'resources/secrets',
        'resources/service',
      ],
    },
    {
      type: 'category',
      label: 'Providers',
      items: [
        'providers/hetzner',
        'providers/cloudflare',
        'providers/neon',
        'providers/infisical',
      ],
    },
    {
      type: 'category',
      label: 'CLI Reference',
      items: [
        'cli/validate',
        'cli/preview',
        'cli/deploy',
        'cli/matrix',
        'cli/plugins',
      ],
    },
    {
      type: 'category',
      label: 'Configuration',
      items: [
        'configuration/inforge-yaml',
        'configuration/environment-yaml',
        'configuration/variables-yaml',
      ],
    },
    {
      type: 'category',
      label: 'GitHub Actions',
      items: [
        'github-actions/overview',
        'github-actions/validate',
        'github-actions/preview',
        'github-actions/deploy',
        'github-actions/reconcile',
        'github-actions/deploy-raw',
      ],
    },
  ],
};

export default sidebars;
