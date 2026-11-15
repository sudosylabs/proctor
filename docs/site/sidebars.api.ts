import type {SidebarsConfig} from '@docusaurus/plugin-content-docs';
import referenceSidebar from '../api/reference/sidebar';
import realtimeSidebar from '../api/reference/realtime-protocol/sidebar';

const sidebars: SidebarsConfig = {
  apiSidebar: [
    {type: 'doc', id: 'index', label: 'Overview'},
    {
      type: 'category',
      label: 'Guides',
      collapsed: true,
      items: [
        'guides/getting-started',
        'guides/authentication',
        'guides/errors',
        'guides/pagination',
        'guides/idempotency',
        'guides/uploads-and-content',
        'guides/realtime',
        'guides/durable-operations',
        'guides/limits-and-compatibility',
      ],
    },
    {
      type: 'category',
      label: 'Recipes',
      collapsed: true,
      items: [
        'recipes/bootstrap-installation',
        'recipes/account-entry-and-recovery',
        'recipes/account-security',
        'recipes/access-policy',
        'recipes/retention-policy',
        'recipes/exam-exports',
        'recipes/desktop-authorization',
        'recipes/academic-structure',
        'recipes/roles-and-scopes',
        'recipes/invite-and-link-users',
        'recipes/onboarding-imports-and-progression',
        'recipes/author-and-publish-exam',
        'recipes/manage-sitting',
        'recipes/candidate-attempt',
        'recipes/review-and-release',
        'recipes/consume-audit',
      ],
    },
    {
      type: 'category',
      label: 'Endpoints',
      collapsed: false,
      items: referenceSidebar,
    },
    {
      type: 'category',
      label: 'Realtime messages',
      collapsed: true,
      items: realtimeSidebar,
    },
  ],
};

export default sidebars;
