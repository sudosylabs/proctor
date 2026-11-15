import MDXComponents from '@theme-original/MDXComponents';
import Heading from '@theme/Heading';
import type {ComponentProps, ReactNode} from 'react';
import {GuideMeta} from '@site/src/theme/DocItem/Content';
import ApiOverviewHero from '@site/src/components/ApiOverviewHero';
import Callout from '@site/src/components/Callout';
import GlossaryIndex from '@site/src/components/GlossaryIndex';
import GovernedFigure from '@site/src/components/GovernedFigure';
import LinkList from '@site/src/components/LinkList';
import Maturity from '@site/src/components/Maturity';
import ProblemIndex from '@site/src/components/ProblemIndex';
import {CodeRows, EndpointRows, ProblemHead} from '@site/src/components/ProblemParts';
import Term from '@site/src/components/Term';

function PageTitle(props: ComponentProps<'h1'>): ReactNode {
  return (
    <>
      <Heading as="h1" {...props} />
      <GuideMeta />
    </>
  );
}

export default {
  ...MDXComponents,
  h1: PageTitle,
  ApiOverviewHero,
  Callout,
  GlossaryIndex,
  GovernedFigure,
  LinkList,
  Maturity,
  CodeRows,
  EndpointRows,
  ProblemHead,
  ProblemIndex,
  Term,
};
