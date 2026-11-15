import Link from '@docusaurus/Link';
import type {ReactNode} from 'react';

import {guides, homeReference} from '@site/navigation.mjs';
import LinkList from '@site/src/components/LinkList';
import {openSearch} from '@site/src/theme/SearchBar';
import styles from './styles.module.css';

const groups = [
  {id: 'use', title: 'Use Proctor'},
  {id: 'build', title: 'Run and build on Proctor'},
];

// The home page is a contents page: one way to search, the guides grouped by
// what the reader is doing, and the reference shelf.
export default function HomeDirectory(): ReactNode {
  return (
    <div className={styles.home}>
      <section className={styles.intro}>
        <h1>Proctor documentation</h1>
        <p className={styles.lead}>
          Guides and reference for using, running and building on Proctor, the open-source exam
          platform you host yourself.
        </p>
        <button className={styles.search} onClick={openSearch} type="button">
          <svg aria-hidden="true" height="16" viewBox="0 0 16 16" width="16">
            <circle cx="7" cy="7" fill="none" r="4.75" stroke="currentColor" strokeWidth="1.5" />
            <path d="M10.5 10.5L14 14" stroke="currentColor" strokeLinecap="round" strokeWidth="1.5" />
          </svg>
          <span>Search guides, settings, endpoints and error codes</span>
          <kbd>⌘K</kbd>
        </button>
      </section>

      <div className={styles.guides}>
        {groups.map((group) => (
          <section aria-labelledby={`home-${group.id}`} key={group.id}>
            <h2 className={styles.label} id={`home-${group.id}`}>
              {group.title}
            </h2>
            <LinkList
              items={guides
                .filter((guide) => guide.group === group.id)
                .map((guide) => ({title: guide.label, description: guide.description, to: guide.to}))}
            />
          </section>
        ))}
      </div>

      <section aria-labelledby="home-reference" className={styles.reference}>
        <h2 className={styles.label} id="home-reference">
          Reference
        </h2>
        <ul>
          {homeReference.map((entry) => (
            <li key={entry.to}>
              <Link to={entry.to}>
                <span className={styles.refTitle}>{entry.label}</span>
                <span className={styles.refDescription}>{entry.description}</span>
              </Link>
            </li>
          ))}
        </ul>
      </section>
    </div>
  );
}
