import Link from '@docusaurus/Link';
import type {ReactNode} from 'react';

import styles from './styles.module.css';

export type LinkListItem = {
  title: ReactNode;
  description?: ReactNode;
  to: string;
};

// A ruled list of destinations: title, one line of description, a chevron.
export default function LinkList({items, mono = false}: {items: LinkListItem[]; mono?: boolean}): ReactNode {
  return (
    <ul className={styles.list}>
      {items.map((item) => (
        <li key={item.to}>
          <Link className={styles.link} to={item.to}>
            <span className={mono ? `${styles.title} ${styles.mono}` : styles.title}>{item.title}</span>
            {item.description ? <span className={styles.description}>{item.description}</span> : null}
            <svg aria-hidden="true" className={styles.chevron} height="16" viewBox="0 0 16 16" width="16">
              <path
                d="M6 3.5L10.5 8 6 12.5"
                fill="none"
                stroke="currentColor"
                strokeLinecap="round"
                strokeLinejoin="round"
                strokeWidth="1.5"
              />
            </svg>
          </Link>
        </li>
      ))}
    </ul>
  );
}
