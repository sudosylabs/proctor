import {useMemo, useState} from 'react';

import {glossaryTerms} from '@site/src/generated/glossary';
import styles from './styles.module.css';

export default function GlossaryIndex(): React.JSX.Element {
  const [query, setQuery] = useState('');
  const matches = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    if (!normalized) {
      return glossaryTerms;
    }
    return glossaryTerms.filter((entry) =>
      `${entry.term} ${entry.section} ${entry.definition} ${entry.avoid}`
        .toLowerCase()
        .includes(normalized),
    );
  }, [query]);
  const sections = [...new Set(matches.map((entry) => entry.section))];

  const filtering = query.trim().length > 0;

  return (
    <div className={styles.glossary}>
      <label className={styles.filter}>
        <svg aria-hidden="true" height="15" viewBox="0 0 16 16" width="15">
          <path d="M2.5 4h11M4.5 8h7M6.5 12h3" stroke="currentColor" strokeLinecap="round" strokeWidth="1.5" />
        </svg>
        <span className={styles.hidden}>Filter terms</span>
        <input
          autoComplete="off"
          name="glossary-query"
          onChange={(event) => setQuery(event.target.value)}
          placeholder="Filter terms"
          spellCheck={false}
          type="search"
          value={query}
        />
      </label>
      <p aria-live="polite" className={styles.count}>
        {filtering ? `${matches.length} of ${glossaryTerms.length} terms` : null}
      </p>
      {sections.map((section) => (
        <section className={styles.section} key={section}>
          <h2>{section}</h2>
          <dl>
            {matches
              .filter((entry) => entry.section === section)
              .map((entry) => (
                <div className={styles.entry} id={entry.id} key={entry.id}>
                  <dt>
                    <a href={`#${entry.id}`}>{entry.term}</a>
                  </dt>
                  <dd>
                    <p>{entry.definition}</p>
                    {entry.avoid ? <p className={styles.avoid}>Avoid: {entry.avoid}</p> : null}
                  </dd>
                </div>
              ))}
          </dl>
        </section>
      ))}
      {matches.length === 0 ? (
        <p className={styles.empty}>No term matches “{query.trim()}”.</p>
      ) : null}
    </div>
  );
}
