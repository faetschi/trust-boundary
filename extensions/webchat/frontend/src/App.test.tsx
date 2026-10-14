import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { App } from './App';

describe('App scaffold', () => {
  it('renders the Phase 1 placeholder heading and the build version', () => {
    const html = renderToStaticMarkup(<App version="0.1.0" />);
    expect(html).toContain('tbound web chat — React scaffold (Phase 1, not the product UI)');
    expect(html).toContain('build 0.1.0');
  });

  it('styles by className only: no inline style attribute and no event-handler attributes', () => {
    const html = renderToStaticMarkup(<App version="0.1.0" />);
    expect(html).toMatch(/<section class="[^"]+" data-scaffold="banner">/);
    expect(html).not.toMatch(/\sstyle\s*=/);
    expect(html).not.toMatch(/\son[a-z]+\s*=/);
  });
});
