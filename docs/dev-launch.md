# Development launch

The former embedded-SDK development fixture is retired. The current
`serve --pi --dev-fixture` starts the official Pi 0.87.1 CLI in RPC mode with
the isolated TBound extension and a scripted offline provider. Use the
environment setup and receipt limits in [launch](launch.md). It exercises four
mediated tool turns and does not establish production qualification, real
provider exchange, settlement, containment, or publication.

The separate Go-only `serve --pi --native-fixture` does not start Pi. The
production CLI profile and launcher remain blocked; see [current
status](status.md).
