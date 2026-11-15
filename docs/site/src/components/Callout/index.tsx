import type {ReactNode} from 'react';

import styles from './styles.module.css';

type Tone = 'note' | 'warn';

// A bordered note. The icon carries the tone; the box is never tinted.
export default function Callout({tone = 'note', children}: {tone?: Tone; children: ReactNode}): ReactNode {
  return (
    <div className={`${styles.callout} ${styles[tone]}`} role="note">
      {tone === 'warn' ? (
        <svg aria-hidden="true" height="18" viewBox="0 0 18 18" width="18">
          <path d="M9 2.5l7 12.5H2L9 2.5z" fill="none" stroke="currentColor" strokeLinejoin="round" strokeWidth="1.5" />
          <path d="M9 7.5v3.5M9 13v.01" stroke="currentColor" strokeLinecap="round" strokeWidth="1.6" />
        </svg>
      ) : (
        <svg aria-hidden="true" height="18" viewBox="0 0 18 18" width="18">
          <circle cx="9" cy="9" fill="none" r="7" stroke="currentColor" strokeWidth="1.5" />
          <path d="M9 8v4.5M9 5.5v.01" stroke="currentColor" strokeLinecap="round" strokeWidth="1.6" />
        </svg>
      )}
      <div>{children}</div>
    </div>
  );
}
