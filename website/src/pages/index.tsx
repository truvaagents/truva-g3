import type {ReactNode} from 'react';
import clsx from 'clsx';
import Link from '@docusaurus/Link';
import useDocusaurusContext from '@docusaurus/useDocusaurusContext';
import Layout from '@theme/Layout';
import Heading from '@theme/Heading';

import styles from './index.module.css';

type Feature = {title: string; body: string};

const FEATURES: Feature[] = [
  {
    title: 'Capability-based discovery',
    body: 'Tools advertise what they do; agents resolve to whichever endpoint serves that capability at runtime. Add, scale, or replace without rewiring callers.',
  },
  {
    title: 'Decentralized coordination',
    body: 'No central conductor. Each agent reads the shared registry, resolves capabilities, and calls peers directly over HTTP/REST.',
  },
  {
    title: 'Vendor-agnostic AI',
    body: 'Switch between OpenAI, Anthropic, Gemini, Groq, or self-hosted models (Ollama, vLLM, llama.cpp) without changing agent code.',
  },
  {
    title: 'Plain Kubernetes, plain HTTP',
    body: 'Ordinary Deployments and Services. No proprietary CRDs, no required sidecars, no control plane. ~15–44MB Go containers; OpenTelemetry, circuit breakers, semantic retry built in.',
  },
];

function Hero(): ReactNode {
  const {siteConfig} = useDocusaurusContext();
  return (
    <header className={styles.hero}>
      <div className={styles.heroContent}>
        <Heading as="h1" className={styles.heroTitle}>
          <span className="brand-truva">Truva</span>
          <span className="brand-g3">G3</span>
        </Heading>
        <p className={styles.heroTagline}>{siteConfig.tagline}</p>
        <div className={styles.heroButtons}>
          <Link
            className={clsx('button button--lg', styles.btnPrimary)}
            to="/docs/intro">
            Get started
          </Link>
          <Link
            className={clsx('button button--lg', styles.btnSecondary)}
            to="https://github.com/truvaagents/truva-g3">
            View on GitHub
          </Link>
        </div>
      </div>
    </header>
  );
}

function WhyTruvaG3(): ReactNode {
  return (
    <section className={styles.features}>
      <Heading as="h2" className={styles.sectionTitle}>
        Why TruvaG3?
      </Heading>
      <div className={styles.featuresGrid}>
        {FEATURES.map((f) => (
          <article key={f.title} className={styles.featureCard}>
            <h3 className={styles.featureTitle}>{f.title}</h3>
            <p className={styles.featureBody}>{f.body}</p>
          </article>
        ))}
      </div>
    </section>
  );
}

export default function Home(): ReactNode {
  const {siteConfig} = useDocusaurusContext();
  return (
    <Layout title={siteConfig.title} description={siteConfig.tagline}>
      <div className={styles.landing}>
        <Hero />
        <WhyTruvaG3 />
      </div>
    </Layout>
  );
}
