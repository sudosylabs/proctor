import Link from '@docusaurus/Link';
import useBaseUrl from '@docusaurus/useBaseUrl';
import ThemedImage from '@theme/ThemedImage';
import type {ReactNode} from 'react';

// The flat "Proctor Docs" lockup: purple mark and ink lettering on the light
// header, all white on the dark one.
export default function NavbarLogo(): ReactNode {
  return (
    <Link aria-label="Proctor Docs home" className="navbar__brand site-brand" to={useBaseUrl('/')}>
      <ThemedImage
        alt="Proctor Docs"
        className="site-brand__lockup"
        height={22}
        sources={{
          light: useBaseUrl('/img/brand/proctor-docs-lockup-light.svg'),
          dark: useBaseUrl('/img/brand/proctor-docs-lockup-white.svg'),
        }}
        width={170}
      />
    </Link>
  );
}
