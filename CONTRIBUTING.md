# Contributing to framedrag

Thanks for your interest in improving framedrag. This document covers the
dev workflow, PR expectations, and the commit convention. The product spec
lives in `docs/SPEC.md`; read it before changing behavior.

## Prerequisites

- Go 1.26 or newer
- `make`
- [golangci-lint](https://golangci-lint.run/) for `make lint`

## Setting up

```sh
git clone https://github.com/framedrag/framedrag.git
cd framedrag
make build
make test
```

The binary lands at `bin/framedrag`.

## Before you open a PR

Run the full local check suite:

```sh
make check      # gofmt check + go vet + go test -race + govulncheck
make lint       # golangci-lint
make tidy-check # go.mod/go.sum are tidy
```

A PR should:

- target a single logical change (new feature, bugfix, refactor, not a mix)
- include tests for any behavior change
- pass `make check` and `make lint` locally

## Non-negotiable project rules

These come from `CLAUDE.md` and `docs/SPEC.md` and are enforced in review:

- **TDD for core packages.** `internal/health` and `internal/normalize` are
  the core of the product; changes there need the failing test written
  first. Golden-file tests cover every parser, with real feed samples
  vendored into `testdata/`.
- **No network access in unit tests.** Fetching is behind the
  `fetch.Fetcher` interface; use a fake. CI has no business talking to feed
  providers.
- **Never fail open.** A `FAILED` feed falls back to its last-good cached
  copy; after `stale_max` days it is dropped loudly, and the run exits
  non-zero if any feed is `FAILED` or `SUSPECT`. Do not weaken this.
- **Determinism everywhere.** Sorted output, stable serialization,
  reproducible runs. Stable diffs are how change detection and review work.
- **CIDR aggregation must cover exactly the same address space as its
  input**, never more, never less. The property test enforces this;
  keep it passing.
- `net/netip` everywhere, never `net.IP`. IPv4 and IPv6 from day one.
- Dependencies are stdlib + cobra + yaml.v3. Adding anything else needs a
  strong reason, stated in the PR.
- `catalog/feeds.json` is vendored from pfBlockerNG upstream; never
  hand-edit it. `framedrag catalog sync` refreshes it.
- API keys live only in the user's local overlay; never committed, logged,
  or sent anywhere except the feed provider.
- No em-dashes in user-facing copy (README, website, CLI help).

## Commit messages

This repo follows [Conventional Commits](https://www.conventionalcommits.org/).
GoReleaser builds release notes from the commit log, so the prefix matters:

| Prefix      | Use for                                |
|-------------|----------------------------------------|
| `feat:`     | new user-visible behavior              |
| `fix:`      | bug fix                                |
| `docs:`     | documentation only                     |
| `refactor:` | internal change, no user-visible diff  |
| `test:`     | test-only change                       |
| `ci:`       | GitHub Actions / release pipeline      |
| `chore:`    | everything else (scripts, meta)        |
| `deps:`     | dependency bump                        |

Breaking changes use `feat!:` or `fix!:` and include a `BREAKING CHANGE:`
footer.

## Proposing a change

- **Bug**: open an issue.
- **Feature**: open a discussion or issue first so the design can be agreed
  before code is written. Check `docs/SPEC.md` section 2 first: DNSBL,
  GUI, and feed re-hosting are explicitly out of scope, and proposals to
  add them will be declined with a pointer to that section.

## Code style

- Godoc comments on every exported identifier (one sentence).
- Comments explain *why*, not *what*: a non-obvious invariant, a
  workaround, a subtle constraint.
- Keep functions small. If a function gets past ~80 lines, split it.
- No dead code, no TODO comments without an issue link.

## Releases

Maintainers tag `vX.Y.Z` on `main`. The `release.yml` workflow runs
GoReleaser, which builds static binaries for linux, darwin, and freebsd
(amd64 and arm64), generates SBOMs, computes checksums, signs the checksums
file with cosign keyless via GitHub OIDC, and publishes a GitHub Release.
No manual release steps, and no long-lived credentials: the workflow uses
only the ambient `GITHUB_TOKEN`. Automation that needs anything more uses a
GitHub App with short-lived installation tokens
(`actions/create-github-app-token`), never a personal access token.

Version numbers follow [Semantic Versioning](https://semver.org/):

- `MAJOR`: breaking output-format, exit-code, or CLI-flag changes
- `MINOR`: new backward-compatible commands or flags
- `PATCH`: bugfixes, internal refactors

Pre-1.0, the public contract is the CLI surface (`docs/SPEC.md` section 11),
the `--json` output shapes, and the exit-code behavior. Breaking changes to
those bump `MINOR`; post-1.0 they bump `MAJOR`.

## Reporting security issues

Do **not** open a public issue. See [SECURITY.md](SECURITY.md).
