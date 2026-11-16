import type {ReactNode} from 'react';
import styles from './styles.module.css';

type MaturityState = 'available' | 'preview' | 'planned';

interface MaturityProps {
  children: ReactNode;
  state: MaturityState;
}

const labels: Record<MaturityState, string> = {
  available: 'Available.',
  preview: 'Pre-release.',
  planned: 'Planned.',
};

// A bordered note, never a tinted panel: the icon carries the tone and the
// label reads as the first words of the sentence.
export default function Maturity({children, state}: MaturityProps): React.JSX.Element {
  return (
    <aside className={`${styles.notice} ${styles[state]}`} aria-label={labels[state].slice(0, -1)}>
      <svg aria-hidden="true" height="18" viewBox="0 0 18 18" width="18">
        <circle cx="9" cy="9" fill="none" r="7" stroke="currentColor" strokeWidth="1.5" />
        <path d="M9 8v4.5M9 5.5v.01" stroke="currentColor" strokeLinecap="round" strokeWidth="1.6" />
      </svg>
      <div>
        <strong>{labels[state]}</strong> {children}
      </div>
    </aside>
  );
}
