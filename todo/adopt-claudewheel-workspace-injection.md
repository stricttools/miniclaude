# Adopt claudewheel's workspace-injection parameter for test isolation

## Context

claudewheel's `resolve_profile(name)` used to hardcode its default workspace (the sole
reader of the `CLAUDEWHEEL_CONFIG_DIR` environment variable), so this project's tests
could only isolate profile resolution by monkeypatching that env var or the workspace
internals.

As of the claudewheel release AFTER 0.27.0, `resolve_profile` accepts a keyword-only
parameter: `resolve_profile(name, *, workspace=None)`. Passing a
`claudewheel.workspace.Workspace` (built via `Workspace.open(root)`) resolves against
that root directly, reading no environment variable at all; `None`/omitted keeps the
old default behavior, so existing calls are unaffected.

## Work

Wherever this project's tests (conftest and friends) monkeypatch the claudewheel
environment or workspace to isolate `resolve_profile`, pass an explicit
`workspace=Workspace.open(tmp_root)` instead. Keep a floor-only dependency on the
claudewheel version that ships the parameter (no upper bound, per fleet policy).

## Effort

Small: a conftest/test-helper change plus the dependency floor bump.
