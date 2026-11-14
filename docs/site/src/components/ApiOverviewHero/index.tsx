import Link from '@docusaurus/Link';

import {searchEntries} from '@site/src/generated/search-index';
import styles from './styles.module.css';

export default function ApiOverviewHero(): React.JSX.Element {
  const endpointCount = searchEntries.filter((entry) => entry.kind === 'endpoint').length;
  const productAreaCount = searchEntries.filter((entry) => entry.kind === 'product-area').length;

  return (
    <header className={styles.hero}>
      <h1>Proctor API</h1>
      <p className={styles.lead}>
        One HTTP and WebSocket contract for everything the web app and Proctor Desktop do. Start
        with authentication and errors, then follow a recipe for the task you are building.
      </p>
      <div className={styles.actions}>
        <Link className="button button--primary" to="/api/guides/getting-started">
          Start an integration
        </Link>
        <a className="button button--secondary" download href="/openapi/openapi.json">
          Download OpenAPI
        </a>
      </div>
      <p className={styles.facts}>
        <span>OpenAPI 3.1</span>
        <span>
          {endpointCount} endpoints in {productAreaCount} areas
        </span>
        <span>These pages never send requests</span>
      </p>
    </header>
  );
}
