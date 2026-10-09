# vaultsite

vaultsite publishes an Obsidian vault as a website using Go templates. It
rewrites note links, creates responsive images, previews locally, and deploys
to S3 with optional CloudFront invalidation.

Start with the [user guide's starter site](USER-GUIDE.md#getting-started),
then explore configuration, note syntax, templates, and deployment.

## Building vaultsite

Install Go **1.27.1 or newer**, then run from the repository root:

```sh
make build
```

This creates `./vaultsite`. To build without Make:

```sh
go build -tags nodynamic -o vaultsite .
```

Keep the `nodynamic` tag: it selects the embedded AVIF decoder. No external
Markdown or image-processing tools are needed at runtime.

Run `./vaultsite -h` for help. The build output is ignored by Git.

Run `make install` to install `vaultsite` in Go's bin directory (`$GOBIN`, or
`$GOPATH/bin`). Add that directory to your `PATH` to run it from anywhere.

## Documentation

- [User guide](USER-GUIDE.md): the command-line and site-authoring reference.
- Go comments document architecture, APIs, and implementation requirements.
  Run `go doc .` for package boundaries and `go doc ./build` for the build pipeline.
- [Contributor instructions](CLAUDE.md): code layout and writing conventions.

## Development

Use Make to run tests and static checks with the required build tag:

```sh
make test
make vet
```

Update the user guide when public behavior changes and code comments when
implementation requirements change.
