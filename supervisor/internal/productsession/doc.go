// Package productsession is the durable catalog of interactive TBound product
// sessions: signed product registration, sessions, turns, request-key
// idempotency, budgets, project ownership, and restart reconciliation. The
// contract is docs/product-contracts.md. The catalog persists only through one
// audit.Journal, never runs effects, and never imports cmd/tbound.
//
// Everything except the error codes and default paths needs the Linux audit
// journal and the Linux output-scope validator, so it builds on Linux only.
package productsession
