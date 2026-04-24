# 3. Vendor Directory Checked Into Version Control

Date: 2026-04-24

## Status

Accepted

## Context

Go projects can either download dependencies at build time (`go mod download`)
or commit the `vendor/` directory. In the Kubernetes ecosystem, committing
`vendor/` is standard practice to ensure reproducible builds without network
access and to provide a complete audit trail of all dependency code.

## Decision

The `vendor/` directory is checked into version control. CI validates vendor
consistency via `make check-vendoring`.

## Consequences

- Reproducible builds without network access
- Complete audit trail of dependency changes in version control
- Larger repository size (~4800 files, ~1.5M lines including vendor)
- File-size analysis tools may flag vendored files (accepted trade-off)
- Dependabot PRs require a manual `go mod vendor` step before merging
