# Contributing

Bug reports, criteria ideas and pull requests are all welcome. If you are unsure whether something fits, open an [issue](https://github.com/BoxBoxJason/gitlab-achievements/issues) first and we can talk it through.

## Getting set up

```bash
git clone https://github.com/BoxBoxJason/gitlab-achievements.git
cd gitlab-achievements
make build
```

You need [Go](https://golang.org/doc/install) (see [go.mod](go.mod) for the version) and Make. For the full check suite, also install [golangci-lint](https://golangci-lint.run/), [gosec](https://github.com/securego/gosec) and [gotestsum](https://github.com/gotestyourself/gotestsum).

Optional: [Docker](https://www.docker.com/) or [Podman](https://podman.io/) to build the container image, and [dependency-check](https://owasp.org/www-project-dependency-check/) to scan dependencies.

## The loop

```bash
make test              # unit tests
make lint              # golangci-lint, including gosec
make dependency-check  # known CVEs in dependencies
make package           # container image
make helm/lint         # lint and render the Helm chart
```

Branch off `main`, keep the tests passing, and open a pull request explaining what changed and why. Commits follow [Conventional Commits](https://www.conventionalcommits.org/), since the changelog is generated from them.

## Code style

Standard Go, `gofmt`ed, with `golangci-lint` clean. The linter config in [.golangci.yml](.golangci.yml) is the actual contract, so if it passes it is fine.

Two things the linter cannot check for you:

- **Comment the why, not the what.** Most of the non-obvious decisions in this codebase are non-obvious because GitLab's API forced them. Those constraints belong next to the code that works around them.
- **Cover the edges.** Anything touching award delivery, deduplication or resumability needs a test, because a bug there is one nobody can see until somebody's EXP is wrong.

## Testing against a real instance

A few tests run against a live GitLab rather than a mock. [docs/achievements-api-behavior.md](docs/achievements-api-behavior.md) has a throwaway instance in one `podman run`, and the environment variables the live tests look for.
