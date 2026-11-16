// Shared reader-facing destinations. Sidebar page order remains in sidebars.ts.
// Keep labels task-oriented: these appear on the homepage and in navigation.
export const guides = [
  {
    label: 'Use your account',
    description: 'Sign in, recover access, and secure your account.',
    to: '/account/',
    sidebarId: 'accountSidebar',
    group: 'use',
  },
  {
    label: 'Take an exam',
    description: 'Prepare Proctor Desktop, complete your work, and submit.',
    to: '/candidate/',
    sidebarId: 'candidateSidebar',
    group: 'use',
  },
  {
    label: 'Manage exams',
    description: 'Author and publish exams, manage sittings, and review submissions.',
    to: '/exam-manager/',
    sidebarId: 'examManagerSidebar',
    group: 'use',
  },
  {
    label: 'Run Proctor',
    description: 'Install, configure, monitor, and maintain a deployment.',
    to: '/operator/',
    sidebarId: 'operatorSidebar',
    group: 'build',
  },
  {
    label: 'Administer an institution',
    description: 'Set up academic structure, people, and access policies.',
    to: '/institution-admin/',
    sidebarId: 'institutionAdminSidebar',
    group: 'build',
  },
  {
    label: 'Review security',
    description: 'Understand authentication, privacy, and trust boundaries.',
    to: '/security/',
    sidebarId: 'securitySidebar',
    group: 'build',
  },
  {
    label: 'Develop with Proctor',
    description: 'Set up a local environment, understand the code, and contribute.',
    to: '/developers/',
    sidebarId: 'developerSidebar',
    group: 'build',
  },
];

export const sourceRepository = 'https://github.com/sudosylabs/Proctor';
export const reportProblemUrl = `${sourceRepository}/issues/new`;

export const referenceLinks = [
  {label: 'Glossary', to: '/glossary/'},
  {label: 'Configuration', to: '/reference/configuration'},
  {label: 'Environment variables', to: '/reference/environment-variables'},
  {label: 'Command line', to: '/reference/command-line'},
];

export const audienceLabels = {
  everyone: 'All readers',
  'account-holder': 'Account guide',
  candidate: 'Candidate guide',
  'exam-manager': 'Exam Manager guide',
  operator: 'Operator guide',
  'institution-administrator': 'Institution administrator guide',
  'security-reviewer': 'Security review guide',
  developer: 'Developer guide',
  'api-consumer': 'API guide',
};

// The reference shelf on the home page.
export const homeReference = [
  {label: 'API reference', description: 'Every endpoint, with its requirements, request and responses.', to: '/api/'},
  {label: 'Error codes', description: 'What each error code means, what causes it and how to resolve it.', to: '/problems/'},
  {label: 'Configuration', description: 'Every server setting, its default and accepted values.', to: '/reference/configuration'},
  {label: 'Environment variables', description: 'Override any setting without editing the file.', to: '/reference/environment-variables'},
  {label: 'Command line', description: 'The proctor commands for operators.', to: '/reference/command-line'},
  {label: 'Glossary', description: 'The terms these docs use with a specific meaning.', to: '/glossary/'},
];
