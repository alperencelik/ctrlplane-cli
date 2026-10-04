# ctrlplane CLI

The command-line client for [ctrlplane.run](https://ctrlplane.run), hosted Kubernetes control planes
for operators.

`ctrlplane` does what the site does, from your terminal: sign in, list, create and delete your
control planes, and point `kubectl` at them. Installed as `kubectl-ctrlplane`, it is also a
kubectl plugin: `kubectl ctrlplane list`.

## Install

With Homebrew:

```sh
brew install alperencelik/tap/ctrlplane
```

Or download the archive for your system from the
[latest release](https://github.com/alperencelik/ctrlplane-cli/releases/latest), unpack it and
put `ctrlplane` on your `PATH`. Or, with Go 1.26 or later:

```sh
go install github.com/alperencelik/ctrlplane-cli/cmd/ctrlplane@latest
```

To also use it as a kubectl plugin, link it as `kubectl-ctrlplane` next to it:

```sh
ln -s "$(command -v ctrlplane)" "$(dirname "$(command -v ctrlplane)")/kubectl-ctrlplane"
```

The commands that run `kubectl` need `kubectl` on your `PATH`. You don't need kubelogin.

## Sign in

```sh
ctrlplane login                    # https://app.ctrlplane.run
```

`--server` points it at another site, but self-hosted platforms aren't supported yet.

Your browser opens to sign in with GitHub, as on the site. The CLI waits on port 8000 (or 18000
if 8000 is busy) for the browser to come back. It stores the platform's URL and your tokens in
your config directory, readable only by you (`~/.config/ctrlplane/config.json` on Linux,
`~/Library/Application Support/ctrlplane/config.json` on macOS), and renews them on its own.
`ctrlplane logout` deletes that file.

## Control planes

```sh
ctrlplane list                     # or: ctrlplane ls
ctrlplane create dev --wait        # creates <you>-dev and waits until it's ready
ctrlplane delete dev               # asks first; --yes to skip
```

As on the site, the names of your control planes start with your user name: `create dev` makes
`alice-dev`. Every command takes either name, `dev` or `alice-dev`.

## Use one with kubectl

```sh
ctrlplane use dev
```

This adds the control plane to your kubeconfig (`$KUBECONFIG`, else `~/.kube/config`) as context
`ctrlplane-alice-dev` and switches to it, so plain `kubectl` works from then on. That context
gets its tokens from `ctrlplane` itself, so you don't sign in a second time.
`ctrlplane kubeconfig dev` prints the same kubeconfig the site downloads (it signs in through
kubelogin), for machines without the CLI.

The CLI runs these on the control plane you last chose with `use`, or on `--cp <name>`. The
output comes straight from kubectl:

```sh
ctrlplane apply -f crds/           # kubectl apply: a file, a directory, or - for stdin
ctrlplane get apis                 # your CRDs
ctrlplane get virtualmachines -A
ctrlplane controllers              # your controllers: ready, restarts, last exit
ctrlplane kubectl -- auth whoami   # any other kubectl command
ctrlplane get apis --cp staging    # another control plane, without switching to it
```

## Build

```sh
go build ./cmd/ctrlplane
go test ./...
```

Releases: push a tag `vX.Y.Z`; GoReleaser builds the binaries and publishes the GitHub Release.

## License

[Apache 2.0](LICENSE)
