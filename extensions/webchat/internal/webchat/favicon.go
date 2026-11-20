package webchat

import _ "embed"

// The mark is embedded rather than added to dist, which Vite rebuilds from the
// frontend and would not copy. This file is the only copy.
//
//go:embed favicon.ico
var faviconICO []byte
