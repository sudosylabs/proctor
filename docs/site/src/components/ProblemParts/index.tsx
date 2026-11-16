import Link from '@docusaurus/Link';
import type {ReactNode} from 'react';

import {MethodLabel, PathText} from '@site/src/components/EndpointPage';
import styles from './styles.module.css';

// Codes break only after a dot or underscore, never inside a word.
function Breakable({text}: {text: string}): ReactNode {
  return (
    <>
      {text.split(/(?<=[._])/).map((part, index) => (
        <span key={index}>
          {part}
          <wbr />
        </span>
      ))}
    </>
  );
}

type HeadProps = {
  code: string;
  prefix: string;
  status: string;
  retry: string;
  retryAfter: number | null;
  websocket: boolean;
  extension: string | null;
};

// The code as the title, its area prefix quieter than the part that names the
// condition, then the facts a caller acts on first.
export function ProblemHead({code, prefix, status, retry, retryAfter, websocket, extension}: HeadProps): ReactNode {
  const rest = code.startsWith(prefix) ? code.slice(prefix.length) : code;
  const tone = status.startsWith('5') || status.startsWith('429') ? styles.warn : '';
  return (
    <header className={styles.head}>
      <h1 className={styles.title}>
        <span className={styles.prefix}>
          <Breakable text={prefix} />
        </span>
        <Breakable text={rest} />
      </h1>
      <p className={styles.meta}>
        <span className={`${styles.status} ${tone}`}>{status}</span>
        <span>
          Retry: {retry.replace(/\.$/, '').replace(/^./, (first) => first.toLowerCase())}
          {retryAfter ? `. Proctor sends Retry-After: ${retryAfter}` : ''}
        </span>
        {websocket ? <span>Also sent over WebSocket</span> : null}
        {extension ? (
          <span>
            Carries <code>{extension}</code>
          </span>
        ) : null}
      </p>
    </header>
  );
}

type EndpointItem = {method: string; path: string; name: string; href: string};

export function EndpointRows({items}: {items: EndpointItem[]}): ReactNode {
  return (
    <div className={styles.rows} data-plain-link>
      {items.map((item) => (
        <Link className={styles.endpoint} key={`${item.method} ${item.path}`} to={item.href}>
          <MethodLabel method={item.method} />
          <span className={styles.endpointName}>{item.name}</span>
          <code className={styles.endpointPath}>
            <PathText path={item.path} />
          </code>
        </Link>
      ))}
    </div>
  );
}

type CodeItem = {code: string; prefix: string; status: number; meaning: string};

export function CodeRows({items}: {items: CodeItem[]}): ReactNode {
  return (
    <div className={styles.rows} data-plain-link>
      {items.map((item) => (
        <CodeRow item={item} key={item.code} />
      ))}
    </div>
  );
}

export function CodeRow({item}: {item: CodeItem}): ReactNode {
  return (
    <Link className={styles.code} to={`/problems/${item.code}`}>
      <span className={styles.codeName}>
        <span className={styles.prefix}>
          <Breakable text={item.prefix} />
        </span>
        <Breakable text={item.code.slice(item.prefix.length)} />
      </span>
      <span className={styles.codeStatus}>{item.status}</span>
      <span className={styles.codeMeaning}>{item.meaning}</span>
    </Link>
  );
}
