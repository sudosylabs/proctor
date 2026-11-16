import {useDoc} from '@docusaurus/plugin-content-docs/client';
import {ThemeClassNames} from '@docusaurus/theme-common';
import EditThisPage from '@theme/EditThisPage';
import type {ReactNode} from 'react';

import {reportProblemUrl} from '@site/navigation.mjs';

// One quiet row above the pager: edit the source, or say what is wrong.
export default function DocItemFooter(): ReactNode {
  const {metadata} = useDoc();
  if (!metadata.editUrl) return null;
  const title = encodeURIComponent(`Docs: ${metadata.title}`);
  return (
    <footer className={`${ThemeClassNames.docs.docFooter} doc-footer`}>
      <EditThisPage editUrl={metadata.editUrl} />
      <a
        className="doc-footer-report"
        href={`${reportProblemUrl}?title=${title}`}
        rel="noopener noreferrer"
        target="_blank">
        Report a problem with this page
      </a>
    </footer>
  );
}
