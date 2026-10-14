import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { App } from './App';
import './tokens.css';

const container = document.getElementById('root');
if (!container) {
  throw new Error('tbound web chat: #root is missing from index.html');
}

createRoot(container).render(
  <StrictMode>
    <App version={__APP_VERSION__} />
  </StrictMode>,
);
