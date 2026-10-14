import styles from './App.module.css';

export interface AppProps {
  version: string;
}

// Phase 1 placeholder. It proves the toolchain (TSX, CSS Modules, build-time
// version) and renders no product UI. Styling is by className only.
export function App({ version }: AppProps) {
  return (
    <main className={styles.shell}>
      <section className={styles.banner} data-scaffold="banner">
        <h1 className={styles.title}>tbound web chat — React scaffold (Phase 1, not the product UI)</h1>
        <p className={styles.meta}>build {version} · served under /app/ · read-only placeholder</p>
      </section>
    </main>
  );
}
