# Changelog

Notable changes, newest first. Releases are git tags; this file says what each tag contains.

## v0.1.0 — initial public release

**The background worker that keeps the fleet's trust cache warm: the only component that talks to the trust service.**

It materialises trust anchors, snapshot freshness and the hottest status lists into a shared cache,
fail-closed. Consumers read that cache and never fetch trust data themselves, so one warm cache absorbs
the whole fleet's trust reads, and a withdrawal takes effect for every reader the moment a new snapshot
is materialised — a per-type atomic replacement, so a reader observes strictly before- or
strictly after-swap state.

It exposes probes and metrics plus a key-gated operator surface; without the key that surface is
unbound.

### Running it

The image entrypoint is `["/server", "web"]`, with `["/server", "health"]` as the healthcheck
command — a compose or orchestrator file that also passes `command: ["web"]` would append a second
argument. Every setting is an environment variable with a safe default where one is possible; the
README lists them, and any secret can be supplied as `<NAME>_FILE` pointing at a mounted file instead.

### What it needs

Go **1.27.0** to build. Libraries at this release:

`go-eudi-trust` v0.1.2 · `go-verifier-helpers` v0.0.4 · `go-authbyte` v0.23.1 ·
`go-platform-kit` v1.11.3 · `azugo.io/azugo` + `azugo.io/core` v0.38.1
