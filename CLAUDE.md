# vaultsite

`vaultsite` publishes an Obsidian vault to the web. [USER-GUIDE.md](USER-GUIDE.md)
defines public behavior; Go comments document architecture, APIs, and
implementation requirements.

## Writing Go code

- Follow [Google's Go style guide](https://google.github.io/styleguide/go/).
- Follow the package layout and import order documented in [main.go](main.go).
- Prefer Go comments to Markdown; document each topic once, beside its code.
- Write GoDoc for callers: behavior, constraints, errors, and ownership.
  Use ordinary comments for implementation rationale and tricky details.
- Explain what code cannot show. Include history only to justify a decision.
