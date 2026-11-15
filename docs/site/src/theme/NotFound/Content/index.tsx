import Link from '@docusaurus/Link';
import {useLocation} from '@docusaurus/router';
import useBaseUrl from '@docusaurus/useBaseUrl';
import {useEffect, useState, type ReactNode} from 'react';

import LinkList, {type LinkListItem} from '@site/src/components/LinkList';
import {openSearch} from '@site/src/theme/SearchBar';
import {searchEntries, type SearchEntry} from '@site/src/generated/search-index';
import styles from './styles.module.css';

function words(value: string): string[] {
  return value.toLowerCase().split(/[^a-z0-9]+/).filter((word) => word.length > 1);
}

function distance(a: string, b: string): number {
  const row = Array.from({length: b.length + 1}, (_, index) => index);
  for (let i = 1; i <= a.length; i += 1) {
    let previous = row[0];
    row[0] = i;
    for (let j = 1; j <= b.length; j += 1) {
      const current = row[j];
      row[j] = Math.min(row[j] + 1, row[j - 1] + 1, previous + (a[i - 1] === b[j - 1] ? 0 : 1));
      previous = current;
    }
  }
  return row[b.length];
}

// How close a known page is to the address the reader asked for: the shape of
// the path first, then shared words in the path and the page title.
function similarity(requested: string, entry: SearchEntry): number {
  const target = entry.href.replace(/\/$/, '').toLowerCase();
  const asked = requested.replace(/\/$/, '').toLowerCase();
  const pathScore = 1 - distance(asked, target) / Math.max(asked.length, target.length, 1);
  const askedWords = new Set(words(asked));
  const entryWords = new Set([...words(target), ...words(entry.title)]);
  const shared = [...askedWords].filter((word) => entryWords.has(word)).length;
  const wordScore = askedWords.size === 0 ? 0 : shared / askedWords.size;
  const sameSection = asked.split('/')[1] === target.split('/')[1] ? 0.1 : 0;
  return pathScore * 0.6 + wordScore * 0.4 + sameSection;
}

function suggestions(pathname: string, baseUrl: string): LinkListItem[] {
  const requested = `/${pathname.slice(baseUrl.length).replace(/^\/+/, '')}`;
  const seen = new Set<string>();
  return searchEntries
    .map((entry) => ({entry, score: similarity(requested, entry)}))
    .filter(({score}) => score > 0.45)
    .sort((a, b) => b.score - a.score)
    .filter(({entry}) => (seen.has(entry.href) ? false : (seen.add(entry.href), true)))
    .slice(0, 3)
    .map(({entry}, index) => ({
      title: entry.title,
      description: `${index === 0 ? 'Closest match' : entry.group}: ${entry.href}`,
      to: entry.href,
    }));
}

// A missing page says what was asked for, offers the nearest real pages, and
// always leaves a way home.
export default function NotFoundContent(): ReactNode {
  const {pathname} = useLocation();
  const baseUrl = useBaseUrl('/');
  const [matches, setMatches] = useState<LinkListItem[]>([]);
  useEffect(() => setMatches(suggestions(pathname, baseUrl)), [pathname, baseUrl]);
  const requested = `/${pathname.slice(baseUrl.length).replace(/^\/+/, '')}`;
  const isProblem = requested.startsWith('/problems/');

  return (
    <main className={styles.page}>
      <div className={styles.inner}>
        <p className={styles.code}>404</p>
        <h1 className={styles.title}>Page not found</h1>
        <p className={styles.lead}>
          Nothing exists at <code>{requested}</code>. The page may have moved, or the link may contain a typo.
        </p>
        <LinkList
          items={[
            ...matches,
            {title: 'Documentation home', description: 'Browse every guide and reference', to: '/'},
          ]}
        />
        {isProblem ? (
          <p className={styles.note}>
            Followed a <code>type</code> link from an error? The code may be newer than these docs. Its{' '}
            <code>detail</code> field still describes it, and <Link to="/problems/">every documented code</Link> is
            listed here.
          </p>
        ) : (
          <p className={styles.note}>
            Or <Link to="/problems/">look up an error code</Link>, or{' '}
            <button className={styles.inlineButton} onClick={openSearch} type="button">
              search the docs
            </button>
            .
          </p>
        )}
      </div>
    </main>
  );
}
