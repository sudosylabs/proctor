import Link from '@docusaurus/Link';
import type {Props} from '@theme/PaginatorNavLink';
import type {ReactNode} from 'react';

function Arrow({next}: {next: boolean}): ReactNode {
  return (
    <svg aria-hidden="true" className="pager-arrow" height="14" viewBox="0 0 14 14" width="14">
      <path
        d={next ? 'M5.5 3.5L9 7l-3.5 3.5' : 'M8.5 3.5L5 7l3.5 3.5'}
        fill="none"
        stroke="currentColor"
        strokeLinecap="round"
        strokeLinejoin="round"
        strokeWidth="1.5"
      />
    </svg>
  );
}

export default function PaginatorNavLink({permalink, title, subLabel, isNext}: Props): ReactNode {
  const next = Boolean(isNext);
  return (
    <Link
      className={`pagination-nav__link ${next ? 'pagination-nav__link--next' : 'pagination-nav__link--prev'}`}
      to={permalink}>
      {subLabel ? (
        <span className="pagination-nav__sublabel">
          {next ? null : <Arrow next={false} />}
          {subLabel}
          {next ? <Arrow next /> : null}
        </span>
      ) : null}
      <span className="pagination-nav__label">{title}</span>
    </Link>
  );
}
