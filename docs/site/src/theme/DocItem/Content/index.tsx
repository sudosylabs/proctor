import {useDoc} from '@docusaurus/plugin-content-docs/client';
import {ThemeClassNames} from '@docusaurus/theme-common';
import Heading from '@theme/Heading';
import MDXContent from '@theme/MDXContent';
import type {Props} from '@theme/DocItem/Content';
import type {ReactNode} from 'react';

import {audienceLabels} from '@site/navigation.mjs';
import styles from './styles.module.css';

const maturityLabels: Record<string, string> = {
  available: 'Available',
  preview: 'Preview guidance',
  planned: 'Planned',
};

export function GuideMeta(): ReactNode {
  const {frontMatter} = useDoc();
  const {audience, maturity} = frontMatter as typeof frontMatter & {
    audience?: unknown;
    maturity?: unknown;
  };
  if (typeof audience !== 'string' || typeof maturity !== 'string') return null;
  return (
    <p className={styles.meta}>
      <span>{audienceLabels[audience as keyof typeof audienceLabels] ?? audience}</span>
      <span className={styles[maturity]}>{maturityLabels[maturity] ?? maturity}</span>
    </p>
  );
}

// The page head is the title, then a quiet line saying who the page is for and
// how settled the behavior is. A title written in the MDX itself gets the same
// line from the h1 override in MDXComponents.
export default function DocItemContent({children}: Props): ReactNode {
  const {metadata, frontMatter, contentTitle} = useDoc();
  const syntheticTitle =
    !frontMatter.hide_title && typeof contentTitle === 'undefined' ? metadata.title : null;

  return (
    <div className={`${ThemeClassNames.docs.docMarkdown} markdown`}>
      {syntheticTitle ? (
        <header>
          <Heading as="h1">{syntheticTitle}</Heading>
          <GuideMeta />
        </header>
      ) : null}
      <MDXContent>{children}</MDXContent>
    </div>
  );
}
