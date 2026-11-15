import Link from '@docusaurus/Link';
import {useCallback, useEffect, useMemo, useRef, useState} from 'react';

import {guides} from '@site/navigation.mjs';
import {searchEntries, type SearchEntry} from '@site/src/generated/search-index';
import styles from './styles.module.css';

const recommendedRoutes = new Set([
  ...guides.map((guide) => guide.to),
  '/api/',
  '/glossary/',
]);
const endpointCount = searchEntries.filter((entry) => entry.kind === 'endpoint').length;

function score(entry: SearchEntry, normalizedQuery: string): number {
  if (!normalizedQuery) {
    return recommendedRoutes.has(entry.href) ? 1 : 0;
  }
  const title = entry.title.toLowerCase();
  const group = entry.group.toLowerCase();
  const location = `${entry.href} ${entry.method ?? ''} ${entry.path ?? ''}`.toLowerCase();
  const tokens = normalizedQuery.split(/\s+/).filter(Boolean);
  if (!tokens.every((token) => entry.searchText.includes(token) || location.includes(token))) {
    return 0;
  }
  let total = entry.kind === 'guide' || entry.kind === 'glossary' ? 8 : 0;
  if (title === normalizedQuery) total += 120;
  if (title.startsWith(normalizedQuery)) total += 70;
  if (title.includes(normalizedQuery)) total += 45;
  if (group.includes(normalizedQuery)) total += 24;
  if (location.includes(normalizedQuery)) total += 35;
  for (const token of tokens) {
    if (title.includes(token)) total += 18;
    if (group.includes(token)) total += 8;
    if (location.includes(token)) total += 12;
    if (entry.searchText.includes(token)) total += 4;
  }
  return total;
}

function groupLabel(entry: SearchEntry): string {
  switch (entry.kind) {
    case 'endpoint':
      return 'Endpoints';
    case 'product-area':
      return 'API areas';
    case 'realtime-message':
      return 'Realtime messages';
    case 'glossary':
      return 'Glossary';
    case 'problem':
      return 'Error codes';
    default:
      return 'Guides';
  }
}

const openSearchEvent = 'proctor:open-search';

// Lets other controls, such as the search field on the home page, open the
// one search dialog instead of mounting a second index.
export function openSearch(): void {
  window.dispatchEvent(new Event(openSearchEvent));
}

export default function SearchBar(): React.JSX.Element {
  const dialogRef = useRef<HTMLDialogElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const resultListRef = useRef<HTMLUListElement>(null);
  const [query, setQuery] = useState('');

  const openSearch = useCallback(() => {
    const dialog = dialogRef.current;
    if (!dialog || dialog.open) return;
    dialog.showModal();
    window.requestAnimationFrame(() => inputRef.current?.focus());
  }, []);

  const closeSearch = useCallback(() => dialogRef.current?.close(), []);

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault();
        openSearch();
      }
    };
    window.addEventListener('keydown', handleKeyDown);
    window.addEventListener(openSearchEvent, openSearch);
    return () => {
      window.removeEventListener('keydown', handleKeyDown);
      window.removeEventListener(openSearchEvent, openSearch);
    };
  }, [openSearch]);

  const matches = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    return searchEntries
      .map((entry) => ({entry, score: score(entry, normalized)}))
      .filter((match) => match.score > 0)
      .sort(
        (left, right) =>
          right.score - left.score || left.entry.title.localeCompare(right.entry.title),
      )
      .slice(0, 12)
      .map((match) => match.entry);
  }, [query]);

  const focusResult = (index: number) => {
    const links = resultListRef.current?.querySelectorAll<HTMLAnchorElement>('a');
    if (!links || links.length === 0) return;
    links[Math.max(0, Math.min(index, links.length - 1))]?.focus();
  };

  const groups = useMemo(() => {
    if (!query.trim()) return matches.length > 0 ? [{label: 'Start here', entries: matches}] : [];
    const ordered: {label: string; entries: SearchEntry[]}[] = [];
    for (const entry of matches) {
      const label = groupLabel(entry);
      let group = ordered.find((candidate) => candidate.label === label);
      if (!group) {
        group = {label, entries: []};
        ordered.push(group);
      }
      group.entries.push(entry);
    }
    return ordered;
  }, [matches, query]);

  let resultIndex = -1;

  return (
    <>
      <button
        aria-label="Search the docs"
        className={styles.trigger}
        onClick={openSearch}
        type="button">
        <SearchIcon />
        <span className={styles.triggerLabel}>Search</span>
        <kbd>⌘K</kbd>
      </button>

      <dialog
        aria-label="Search the docs"
        className={styles.dialog}
        onCancel={closeSearch}
        onClose={() => setQuery('')}
        onKeyDown={(event) => {
          if (event.key === 'Escape') {
            event.preventDefault();
            closeSearch();
          }
        }}
        ref={dialogRef}>
        <label className={styles.searchField}>
          <SearchIcon />
          <span className={styles.visuallyHidden}>Search</span>
          <input
            autoComplete="off"
            name="docs-query"
            onChange={(event) => setQuery(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === 'ArrowDown') {
                event.preventDefault();
                focusResult(0);
              }
            }}
            placeholder={`Search guides, settings, ${endpointCount} endpoints and error codes`}
            ref={inputRef}
            spellCheck={false}
            type="search"
            value={query}
          />
          <button className={styles.close} onClick={closeSearch} type="button">
            <kbd>Esc</kbd>
            <span className={styles.visuallyHidden}>Close search</span>
          </button>
        </label>

        <div aria-live="polite" className={styles.results}>
          {groups.length > 0 ? (
            <ul ref={resultListRef}>
              {groups.map((group) => (
                <li key={group.label}>
                  <div className={styles.groupLabel}>
                    {group.label}
                  </div>
                  <ul>
                    {group.entries.map((entry) => {
                      resultIndex += 1;
                      const index = resultIndex;
                      return (
                        <li key={entry.id}>
                          <Link
                            className={styles.result}
                            onClick={closeSearch}
                            onKeyDown={(event) => {
                              if (event.key === 'ArrowDown') {
                                event.preventDefault();
                                focusResult(index + 1);
                              } else if (event.key === 'ArrowUp') {
                                event.preventDefault();
                                if (index === 0) inputRef.current?.focus();
                                else focusResult(index - 1);
                              }
                            }}
                            to={entry.href}>
                            {entry.method && entry.path ? (
                              <span className={styles.endpoint}>
                                <b>{entry.method}</b>
                                <code>{entry.path}</code>
                              </span>
                            ) : (
                              <span className={entry.kind === 'problem' ? styles.code : styles.title}>
                                {entry.title}
                              </span>
                            )}
                            <span className={styles.where}>
                              {entry.method && entry.path ? entry.title : entry.description || entry.group}
                            </span>
                          </Link>
                        </li>
                      );
                    })}
                  </ul>
                </li>
              ))}
            </ul>
          ) : (
            <div className={styles.empty}>
              <strong>Nothing matches “{query.trim()}”.</strong>
              <span>Try a task, a setting name, an error code or part of an endpoint path.</span>
            </div>
          )}
        </div>

        <div className={styles.footer}>
          <span><kbd>↑</kbd> <kbd>↓</kbd> to move</span>
          <span><kbd>↵</kbd> to open</span>
          <span className={styles.private}>Runs in your browser; nothing is sent</span>
        </div>
      </dialog>
    </>
  );
}

function SearchIcon(): React.JSX.Element {
  return (
    <svg aria-hidden="true" height="16" viewBox="0 0 16 16" width="16">
      <circle cx="7" cy="7" fill="none" r="4.75" stroke="currentColor" strokeWidth="1.5" />
      <path d="M10.5 10.5L14 14" stroke="currentColor" strokeLinecap="round" strokeWidth="1.5" />
    </svg>
  );
}
