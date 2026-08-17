# fm-provider-test-harness

Shared acceptance-test harness for the Dynatrace Feature Management [OpenFeature](https://openfeature.dev)
providers (Java, Go, Python).

Contains:

- **[`gherkin/`](gherkin)** - a language-agnostic Gherkin spec describing the behavior every provider
  must implement (startup, key validation, CDN config fetching, conditional revalidation, rate
  limiting, basic evaluation).
- **[`mockserver/`](mockserver)** - a dependency-free Go service that emulates the backends a provider
  communicates with (CDN config endpoint, metrics ingest, SSE stream) plus an HTTP control plane to script
  responses and assert on what the provider sent. See the [mock server README](mockserver/README.md).

## Usage

Provider repos embed this repo as a git submodule, run the Gherkin features against their own step
definitions, and start the mock server from the published image:

```bash
docker pull "ghcr.io/dynatrace/fm-provider-mock-server:v$(cat version.txt)"
```

`version.txt` is bumped atomically with each release tag, so the image can never drift from the spec
commit the submodule is pinned to.

To run the mock server locally:

```bash
docker compose up
```

## Contributing

Gherkin files are formatted with Prettier; run `npm run format` before opening a PR. Commit messages
follow [Conventional Commits](https://www.conventionalcommits.org) - releases are cut automatically
by [release-please](https://github.com/googleapis/release-please).

## License

Apache License 2.0 - see [LICENSE](LICENSE).
