import type { SidebarsConfig } from '@docusaurus/plugin-content-docs';

/**
 * Manual sidebar — the three MUST sections (Quickstart + API Reference + Cookbook,
 * Story title / FR-10.3) plus the SDK install index (10.2/10.3/10.4). BR-10.5.3.
 * Other front-end-spec IA pages (/models, /changelog) are cut-line (OQ-10.5-6);
 * /errors + /migration are folded into API Reference + Quickstart respectively.
 */
const sidebars: SidebarsConfig = {
  docs: [
    'intro',
    'quickstart',
    'api-reference',
    'cookbook',
    {
      type: 'category',
      label: 'SDKs',
      link: { type: 'doc', id: 'sdk/index' },
      items: ['sdk/python', 'sdk/typescript', 'sdk/go'],
    },
  ],
};

export default sidebars;
