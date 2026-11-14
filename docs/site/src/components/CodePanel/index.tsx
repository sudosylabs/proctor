import CodeBlock from '@theme/CodeBlock';
import {useState, type ReactNode} from 'react';

import styles from './styles.module.css';

export type CodePanelTab = {
  id: string;
  label: ReactNode;
  language: string;
  code: string;
  highlight?: string;
  tone?: 'ok' | 'bad';
  note?: ReactNode;
};

function CopyIcon(): ReactNode {
  return (
    <svg aria-hidden="true" height="13" viewBox="0 0 14 14" width="13">
      <rect fill="none" height="7.5" rx="1.5" stroke="currentColor" strokeWidth="1.3" width="7.5" x="4.5" y="4.5" />
      <path d="M9.5 2.5H3.5a1 1 0 0 0-1 1v6" fill="none" stroke="currentColor" strokeLinecap="round" strokeWidth="1.3" />
    </svg>
  );
}

// One frame for a set of related snippets: a tab row, a copy action, the code
// with line numbers, and an optional note underneath.
export default function CodePanel({tabs, label}: {tabs: CodePanelTab[]; label: string}): ReactNode {
  const [selected, setSelected] = useState(tabs[0]?.id);
  const [copied, setCopied] = useState(false);
  const current = tabs.find((tab) => tab.id === selected) ?? tabs[0];
  if (!current) return null;

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(current.code);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1600);
    } catch {
      setCopied(false);
    }
  };

  return (
    <div className={styles.panel}>
      <div className={styles.head}>
        <div aria-label={label} className={styles.tabs} role="tablist">
          {tabs.map((tab) => (
            <button
              aria-selected={tab.id === current.id}
              className={tab.tone ? `${styles.tab} ${styles.status} ${styles[tab.tone]}` : styles.tab}
              key={tab.id}
              onClick={() => setSelected(tab.id)}
              role="tab"
              type="button">
              {tab.tone ? <span aria-hidden="true" className={styles.dot} /> : null}
              {tab.label}
            </button>
          ))}
        </div>
        <button aria-label={`Copy ${label.toLowerCase()}`} className={styles.copy} onClick={copy} type="button">
          <CopyIcon />
          {copied ? 'Copied' : 'Copy'}
        </button>
      </div>
      <div className={styles.body}>
        <CodeBlock language={current.language} metastring={current.highlight || undefined} showLineNumbers>
          {current.code}
        </CodeBlock>
      </div>
      {current.note ? <div className={styles.foot}>{current.note}</div> : null}
    </div>
  );
}
