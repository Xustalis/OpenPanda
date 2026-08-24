# Homebrew tap publishing

The public command is intentionally stable:

```sh
brew tap Xustalis/openpanda && brew install openpanda
```

Homebrew resolves it to the public GitHub repository
`Xustalis/homebrew-openpanda`, with the formula at
`Formula/openpanda.rb`. The repository must exist; a formula stored only in
the OpenPanda source repository cannot satisfy this command.

One-time setup:

1. Create the public repository `Xustalis/homebrew-openpanda`.
2. Add an Actions secret named `HOMEBREW_TAP_TOKEN` to `Xustalis/OpenPanda`.
   The token needs permission to push repository contents to the tap.
3. Put the generated `dist/openpanda.rb` at `Formula/openpanda.rb` for the
   current release, or rerun the release workflow after configuring the token.

Every tagged OpenPanda release then renders a checksum-pinned formula and
pushes it to the tap automatically. The formula supports Intel and Apple
Silicon macOS plus amd64 and arm64 Linux.
