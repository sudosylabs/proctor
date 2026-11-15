import useBrokenLinks from '@docusaurus/useBrokenLinks';
import {useMemo, useState, type ReactNode} from 'react';

import {CodeRow} from '@site/src/components/ProblemParts';
import styles from './styles.module.css';

type Problem = {
  code: string;
  area: string;
  areaLabel: string;
  subgroup: string;
  subgroupLabel: string;
  status: number;
  meaning: string;
};

type Props = {problems: Problem[]; areas: number; retryable: number};

// Areas with more codes than this get sub-headings from the next segment.
const subgroupThreshold = 12;

function statusKey(status: number): string {
  if (status >= 500) return '5xx';
  if ([400, 401, 403, 404, 409, 410, 429].includes(status)) return String(status);
  return 'other';
}

// Every code, grouped by area, filtered by status and by text.
export default function ProblemIndex({problems, areas, retryable}: Props): ReactNode {
  const brokenLinks = useBrokenLinks();
  for (const area of new Set(problems.map((problem) => problem.area))) brokenLinks.collectAnchor(area);
  const [query, setQuery] = useState('');
  const [status, setStatus] = useState('all');

  const filters = useMemo(() => {
    const counts = new Map<string, number>();
    for (const problem of problems) counts.set(statusKey(problem.status), (counts.get(statusKey(problem.status)) ?? 0) + 1);
    const otherStatuses = [...new Set(problems.filter((problem) => statusKey(problem.status) === 'other').map((problem) => problem.status))].sort();
    const order = ['400', '401', '403', '404', '409', '410', '429', '5xx', 'other'];
    return [
      {key: 'all', label: 'All', count: problems.length},
      ...order.filter((key) => counts.has(key)).map((key) => ({key, label: key === 'other' ? otherStatuses.join(', ') : key, count: counts.get(key) ?? 0})),
    ];
  }, [problems]);

  const groups = useMemo(() => {
    const tokens = query.trim().toLowerCase().split(/\s+/).filter(Boolean);
    const byArea = new Map<string, {label: string; total: number; items: Problem[]}>();
    for (const problem of problems) {
      if (!byArea.has(problem.area)) byArea.set(problem.area, {label: problem.areaLabel, total: 0, items: []});
      const area = byArea.get(problem.area)!;
      area.total += 1;
      const statusMatch = status === 'all' || statusKey(problem.status) === status;
      const textMatch = tokens.every((token) => problem.code.includes(token) || problem.meaning.toLowerCase().includes(token));
      if (statusMatch && textMatch) area.items.push(problem);
    }
    return [...byArea.entries()]
      .filter(([, area]) => area.items.length > 0)
      .sort(([, a], [, b]) => a.label.localeCompare(b.label));
  }, [problems, query, status]);

  const shown = groups.reduce((total, [, area]) => total + area.items.length, 0);
  const filtering = query.trim() !== '' || status !== 'all';

  return (
    <div className={styles.index} data-plain-link data-problem-index>
      <p className={styles.summary}>
        {problems.length} codes in {areas} areas. {retryable} are worth retrying after a wait.
      </p>

      <div className={styles.controls}>
        <label className={styles.field}>
          <svg aria-hidden="true" height="15" viewBox="0 0 16 16" width="15">
            <circle cx="7" cy="7" fill="none" r="4.75" stroke="currentColor" strokeWidth="1.5" />
            <path d="M10.5 10.5L14 14" stroke="currentColor" strokeLinecap="round" strokeWidth="1.5" />
          </svg>
          <span className={styles.hidden}>Filter error codes</span>
          <input
            onChange={(event) => setQuery(event.target.value)}
            placeholder="Filter by code or meaning, such as revision_conflict"
            type="search"
            value={query}
          />
        </label>
        <div aria-label="HTTP status" className={styles.statuses} role="group">
          {filters.map((filter) => (
            <button aria-pressed={status === filter.key} key={filter.key} onClick={() => setStatus(filter.key)} type="button">
              {filter.label}
              <span>{filter.count}</span>
            </button>
          ))}
        </div>
      </div>

      <p aria-live="polite" className={styles.count}>
        {filtering ? `${shown} matching ${shown === 1 ? 'code' : 'codes'}` : null}
      </p>

      {groups.map(([area, group]) => {
        const subgroups = group.total > subgroupThreshold;
        const sections = new Map<string, {label: string; items: Problem[]}>();
        for (const problem of group.items) {
          const key = subgroups ? problem.subgroup : '';
          if (!sections.has(key)) sections.set(key, {label: problem.subgroupLabel || 'General', items: []});
          sections.get(key)!.items.push(problem);
        }
        const ordered = [...sections.entries()].sort(([a], [b]) => (a === '' ? -1 : b === '' ? 1 : a.localeCompare(b)));
        return (
          <section aria-labelledby={`area-${area}`} className={styles.area} id={area} key={area}>
            <div className={styles.areaHead}>
              <h2 id={`area-${area}`}>{group.label}</h2>
              <span>
                {group.total} {group.total === 1 ? 'code' : 'codes'}
              </span>
            </div>
            {ordered.map(([key, section]) => (
              <div key={key || 'general'}>
                {subgroups ? <div className={styles.subHead}>{section.label}</div> : null}
                <div className={styles.rows}>
                  {section.items.map((problem) => (
                    <CodeRow
                      item={{code: problem.code, prefix: `${problem.area}.`, status: problem.status, meaning: problem.meaning}}
                      key={problem.code}
                    />
                  ))}
                </div>
              </div>
            ))}
          </section>
        );
      })}

      {groups.length === 0 ? <p className={styles.empty}>No code matches. Try part of the code, such as rate_limited.</p> : null}
    </div>
  );
}
