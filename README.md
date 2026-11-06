# vaultsite

vaultsite publishes an Obsidian vault as a website using Go templates. It
rewrites note links, creates responsive images, previews locally, and deploys
to S3 with optional CloudFront invalidation.

Create your first site with the [user guide](USER-GUIDE.md#getting-started).

## Building vaultsite

Install Go **1.27.1 or newer**, then run from the repository root:

```sh
make build
```

This creates `./vaultsite`. To build without Make:

```sh
go build -o vaultsite .
```

No external Markdown or image-processing tools are needed at runtime.

`make install` installs `vaultsite` in Go's bin directory (`$GOBIN`, or
`$GOPATH/bin`). Add that directory to your `PATH` and run `vaultsite -h`
for help.

## Documentation

- [User guide](USER-GUIDE.md): the command-line and site-authoring reference.
- Run `go doc ./<package>` for a package's API.
- [Contributor instructions](CLAUDE.md): code layout and writing conventions.

## Development

Run tests and static checks:

```sh
make test
make vet
```

Keep the user guide and code comments current when behavior changes.
