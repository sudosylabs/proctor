import Link from '@docusaurus/Link';
import type {ReactNode} from 'react';

import {MethodLabel, PathText} from '@site/src/components/EndpointPage';
import styles from './styles.module.css';

type Area = {
  name: string;
  slug: string;
  description: string;
  operations: {id: string; name: string; method: string; path: string; summary: string}[];
};

// One API area: what it covers, then every endpoint in contract order.
export default function AreaPage({data}: {data: Area}): ReactNode {
  return (
    <div className={styles.area} data-plain-link>
      <header className={styles.head}>
        <h1>{data.name}</h1>
        {data.description ? <p className={styles.lead}>{data.description}</p> : null}
        <p className={styles.meta}>
          {data.operations.length} {data.operations.length === 1 ? 'endpoint' : 'endpoints'}
        </p>
      </header>
      <div className={styles.list}>
        {data.operations.map((operation) => (
          <Link className={styles.row} key={operation.id} to={`/api/reference/${operation.id}`}>
            <MethodLabel method={operation.method} />
            <span className={styles.name}>{operation.name}</span>
            <code className={styles.path}>
              <PathText path={operation.path} />
            </code>
            {operation.summary ? <span className={styles.summary}>{operation.summary}</span> : null}
          </Link>
        ))}
      </div>
    </div>
  );
}
