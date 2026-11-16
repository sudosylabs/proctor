import Link from '@docusaurus/Link';
import type {ReactNode} from 'react';

import {reportProblemUrl, sourceRepository} from '@site/navigation.mjs';

export default function Footer(): ReactNode {
  return (
    <footer className="site-footer">
      <div className="site-footer__inner">
        <span>Proctor is open source and self-hosted.</span>
        <nav aria-label="Footer">
          <Link href={sourceRepository}>Source</Link>
          <Link href={reportProblemUrl}>Report a docs problem</Link>
          <Link href={`${sourceRepository}/blob/main/CONTRIBUTING.md`}>Contributing</Link>
          <Link href={`${sourceRepository}/blob/main/LICENSING.md`}>Licensing</Link>
        </nav>
      </div>
    </footer>
  );
}
