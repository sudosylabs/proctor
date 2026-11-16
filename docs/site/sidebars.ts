import type {SidebarsConfig} from '@docusaurus/plugin-content-docs';

const sidebars: SidebarsConfig = {
  homeSidebar: [
    {type: 'doc', id: 'index', label: 'Overview'},
  ],
  accountSidebar: [
    {
      type: 'category',
      label: 'Use your account',
      collapsed: false,
      collapsible: false,
      link: {type: 'doc', id: 'account/index'},
      items: [
        {type: 'doc', id: 'account/access', label: 'Sign in and recover access'},
        {type: 'doc', id: 'account/security', label: 'Secure your account'},
        {type: 'doc', id: 'account/desktop-authorization', label: 'Authorize Proctor Desktop'},
      ],
    },
  ],
  candidateSidebar: [
    {
      type: 'category',
      label: 'Take an exam',
      collapsed: false,
      collapsible: false,
      link: {type: 'doc', id: 'candidate/index'},
      items: [
        {type: 'doc', id: 'candidate/prepare-and-enter', label: 'Prepare and enter'},
        {type: 'doc', id: 'candidate/workspace-and-continuity', label: 'Workspace and continuity'},
        {type: 'doc', id: 'candidate/browser-and-privacy', label: 'Browser and privacy'},
        {type: 'doc', id: 'candidate/submit-and-recover', label: 'Submit and recover'},
        {type: 'doc', id: 'candidate/released-result', label: 'Released results'},
      ],
    },
  ],
  operatorSidebar: [
    {
      type: 'category',
      label: 'Run Proctor',
      collapsed: false,
      collapsible: false,
      link: {type: 'doc', id: 'operator/index'},
      items: [
        {type: 'doc', id: 'operator/package-and-install', label: 'Package and install'},
        {type: 'doc', id: 'operator/configuration-and-secrets', label: 'Configuration and secrets'},
        {type: 'doc', id: 'operator/systemd-and-container', label: 'systemd or a container'},
        {type: 'doc', id: 'operator/topology-and-dependencies', label: 'Topology and dependencies'},
        {type: 'doc', id: 'operator/tls-and-proxy', label: 'TLS and reverse proxies'},
        {type: 'doc', id: 'operator/health-and-observability', label: 'Health and observability'},
        {type: 'doc', id: 'operator/mail-delivery', label: 'Mail delivery'},
        {type: 'doc', id: 'operator/maintenance-and-recovery', label: 'Maintenance and recovery'},
        {type: 'doc', id: 'operator/backup-restore-and-upgrade', label: 'Backup, restore and upgrade'},
        {type: 'doc', id: 'operator/pre-production-readiness', label: 'Before production'},
      ],
    },
  ],
  institutionAdminSidebar: [
    {
      type: 'category',
      label: 'Administer an Institution',
      collapsed: false,
      collapsible: false,
      link: {type: 'doc', id: 'institution-admin/index'},
      items: [
        {type: 'doc', id: 'institution-admin/access-policy', label: 'Access Policy'},
        {type: 'doc', id: 'institution-admin/academic-structure', label: 'Academic structure'},
        {type: 'doc', id: 'institution-admin/people-and-authority', label: 'People and scoped authority'},
        {type: 'doc', id: 'institution-admin/onboarding', label: 'Invitations and onboarding'},
        {type: 'doc', id: 'institution-admin/imports-and-progression', label: 'Imports and progression'},
        {type: 'doc', id: 'institution-admin/audit-history', label: 'Audit history'},
      ],
    },
  ],
  examManagerSidebar: [
    {
      type: 'category',
      label: 'Manage exams',
      collapsed: false,
      collapsible: false,
      link: {type: 'doc', id: 'exam-manager/index'},
      items: [
        {type: 'doc', id: 'exam-manager/author-and-publish', label: 'Author and publish'},
        {type: 'doc', id: 'exam-manager/resources-and-workspace', label: 'Resources and Starter Workspace'},
        {type: 'doc', id: 'exam-manager/sitting-operations', label: 'Sitting operations'},
        {type: 'doc', id: 'exam-manager/corrections-and-attempt-control', label: 'Corrections and Attempt control'},
        {type: 'doc', id: 'exam-manager/submission-and-integrity-review', label: 'Submission and Integrity Review'},
        {type: 'doc', id: 'exam-manager/finalize-and-release', label: 'Finalize and release'},
      ],
    },
  ],
  securitySidebar: [
    {
      type: 'category',
      label: 'Review security',
      collapsed: false,
      collapsible: false,
      link: {type: 'doc', id: 'security/index'},
      items: [
        {type: 'doc', id: 'security/threat-model', label: 'Threat model and trust boundaries'},
        {type: 'doc', id: 'security/credentials-authorization', label: 'Credentials and authorization'},
        {type: 'doc', id: 'security/data-privacy', label: 'Data and privacy'},
        {type: 'doc', id: 'security/keys-network', label: 'Keys, certificates and network'},
        {type: 'doc', id: 'security/content-workspace', label: 'Content and Workspace'},
        {type: 'doc', id: 'security/errors-limits-compatibility', label: 'Errors, limits and compatibility'},
      ],
    },
  ],
  developerSidebar: [
    {
      type: 'category',
      label: 'Developer guide',
      collapsed: false,
      collapsible: false,
      link: {type: 'doc', id: 'developers/index'},
      items: [
        {type: 'doc', id: 'developers/local-setup', label: 'Local setup'},
        {
          type: 'doc',
          id: 'developers/local-services',
          label: 'Local services',
        },
        {
          type: 'doc',
          id: 'developers/cluster-development',
          label: 'Cluster development',
        },
        {
          type: 'doc',
          id: 'developers/development-configuration',
          label: 'Development configuration',
        },
        {
          type: 'doc',
          id: 'developers/make-commands',
          label: 'Make commands',
        },
        {
          type: 'doc',
          id: 'developers/tooling-and-debugging',
          label: 'Tooling and debugging',
        },
        {
          type: 'doc',
          id: 'developers/repository-boundaries',
          label: 'Repository boundaries',
        },
        {
          type: 'category',
          label: 'Server development',
          collapsed: true,
          link: {
            type: 'doc',
            id: 'developers/server-workflow',
          },
          items: [
            {
              type: 'doc',
              id: 'developers/server/architecture-composition',
              label: 'Architecture and composition',
            },
            {
              type: 'doc',
              id: 'developers/server/domain-application',
              label: 'Domain and application',
            },
            {
              type: 'doc',
              id: 'developers/persistence',
              label: 'Persistence and transactions',
            },
            {
              type: 'doc',
              id: 'developers/http-and-openapi',
              label: 'HTTP and OpenAPI',
            },
            {
              type: 'doc',
              id: 'developers/server/authorization-errors-audit',
              label: 'Authorization, errors and audit',
            },
            {
              type: 'doc',
              id: 'developers/server/desktop-trust-admission',
              label: 'Desktop trust and admission',
            },
            {
              type: 'doc',
              id: 'developers/server/jobs-mail',
              label: 'Jobs and transactional mail',
            },
            {
              type: 'doc',
              id: 'developers/server/files-workspaces',
              label: 'Files and Workspaces',
            },
            {
              type: 'doc',
              id: 'developers/server/attempt-continuity',
              label: 'Attempt continuity',
            },
            {
              type: 'doc',
              id: 'developers/server/browser-activity-submission',
              label: 'Browser activity and Submission',
            },
            {
              type: 'doc',
              id: 'developers/server/integrity-review-release',
              label: 'Integrity Review and release',
            },
            {
              type: 'doc',
              id: 'developers/server/realtime-cluster-effects',
              label: 'Realtime, cluster and effects',
            },
            {
              type: 'doc',
              id: 'developers/server/runtime-integrations',
              label: 'Runtime and integrations',
            },
            {
              type: 'doc',
              id: 'developers/server/plugins',
              label: 'Plugins',
            },
            {
              type: 'doc',
              id: 'developers/server/execution-environment',
              label: 'Execution Environment',
            },
            {
              type: 'doc',
              id: 'developers/server/user-settings-slice',
              label: 'A complete vertical slice',
            },
            {
              type: 'doc',
              id: 'developers/server/review-checklists',
              label: 'Review checklists',
            },
          ],
        },
        {
          type: 'category',
          label: 'Webapp development',
          collapsed: true,
          link: {
            type: 'doc',
            id: 'developers/webapp-workflow',
          },
          items: [
            {
              type: 'doc',
              id: 'developers/webapp/architecture-routing',
              label: 'Architecture and routing',
            },
            {
              type: 'doc',
              id: 'developers/webapp/features-api-localization',
              label: 'Features, API and localization',
            },
            {
              type: 'doc',
              id: 'developers/webapp/states-forms-accessibility',
              label: 'States, forms and accessibility',
            },
            {
              type: 'doc',
              id: 'developers/webapp/testing',
              label: 'Testing the webapp',
            },
            {
              type: 'doc',
              id: 'developers/webapp/visual-review',
              label: 'Visual review',
            },
            {
              type: 'doc',
              id: 'developers/webapp/password-reset-slice',
              label: 'A complete vertical slice',
            },
            {
              type: 'doc',
              id: 'developers/webapp/review-checklists',
              label: 'Review checklists',
            },
          ],
        },
        {
          type: 'doc',
          id: 'developers/reusable-modules',
          label: 'Reusable modules',
        },
        {
          type: 'doc',
          id: 'developers/testing',
          label: 'Testing and verification',
        },
        {
          type: 'category',
          label: 'Troubleshooting',
          collapsed: true,
          link: {
            type: 'doc',
            id: 'developers/troubleshooting/index',
          },
          items: [
            {
              type: 'doc',
              id: 'developers/troubleshooting/runtime-readiness',
              label: 'Runtime and readiness',
            },
            {
              type: 'doc',
              id: 'developers/troubleshooting/http-access',
              label: 'HTTP access',
            },
            {
              type: 'doc',
              id: 'developers/troubleshooting/data-content',
              label: 'Data and content',
            },
            {
              type: 'doc',
              id: 'developers/troubleshooting/mail',
              label: 'Transactional mail',
            },
            {
              type: 'doc',
              id: 'developers/troubleshooting/jobs-realtime',
              label: 'Jobs and realtime',
            },
            {
              type: 'doc',
              id: 'developers/troubleshooting/browser',
              label: 'Browser application',
            },
            {
              type: 'doc',
              id: 'developers/troubleshooting/cluster',
              label: 'Cluster failures',
            },
          ],
        },
        {
          type: 'doc',
          id: 'developers/documentation',
          label: 'Documentation',
        },
        {
          type: 'doc',
          id: 'developers/contribution-checklist',
          label: 'Contribution checklist',
        },
      ],
    },
  ],
  referenceSidebar: [
    {
      type: 'category',
      label: 'Reference',
      collapsed: false,
      collapsible: false,
      items: [
        {type: 'doc', id: 'reference/glossary', label: 'Glossary'},
        {
          type: 'category',
          label: 'Configuration',
          collapsed: true,
          link: {type: 'doc', id: 'reference/configuration'},
          items: [
            {
              type: 'doc',
              id: 'reference/configuration/server-and-metrics',
              label: 'Server and metrics',
            },
            {
              type: 'doc',
              id: 'reference/configuration/data-and-cluster',
              label: 'Data and cluster',
            },
            {
              type: 'doc',
              id: 'reference/configuration/mail-and-files',
              label: 'Mail and files',
            },
            {
              type: 'doc',
              id: 'reference/configuration/authentication',
              label: 'Authentication',
            },
            {
              type: 'doc',
              id: 'reference/configuration/localization-and-logging',
              label: 'Localization and logging',
            },
            {
              type: 'doc',
              id: 'reference/configuration/execution',
              label: 'Execution',
            },
            {
              type: 'doc',
              id: 'reference/configuration/plugins',
              label: 'Plugins',
            },
          ],
        },
        {type: 'doc', id: 'reference/environment-variables', label: 'Environment variables'},
        {type: 'doc', id: 'reference/command-line', label: 'Command line'},
        {
          type: 'link',
          href: 'https://github.com/sudosylabs/Proctor/tree/main/docs/architecture',
          label: 'Architecture source',
        },
      ],
    },
  ],
};

export default sidebars;
