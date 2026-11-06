# vaultsite

`vaultsite` publishes an Obsidian vault to the web. [USER-GUIDE.md](USER-GUIDE.md)
defines public behavior; Go comments document APIs and implementation constraints.

## Writing Go code

- Follow [Google's Go style guide](https://google.github.io/styleguide/go/).
- Follow the package layout and import order documented in [main.go](main.go).
- Document each topic once, beside its code.
- Write Go doc comments for callers: behavior, constraints, errors, and ownership.
- Keep prose simple and concise. Remove comments that narrate the code.
- Use ordinary comments to explain decisions and nonobvious constraints.
