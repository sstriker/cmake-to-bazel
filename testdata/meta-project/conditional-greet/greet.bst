kind: manual

sources:
- kind: local
  path: sources

# Per-arch conditional variable overrides — write-a lowers this
# block into project-B `cmd = select({...})` so Bazel resolves
# per target platform, instead of baking write-a's host arch into
# the rendered cmd.
variables:
  arch-marker: 'unknown'
  (?):
  - target_arch == "x86_64":
      arch-marker: 'x86_64'
  - target_arch == "aarch64":
      arch-marker: 'aarch64'
  - target_arch in ("ppc64le", "ppc64"):
      arch-marker: 'ppc'

config:
  install-commands:
  # %{arch-marker} resolves per arch through the select(). The
  # rendered cmd is one entry of the select() dict per supported
  # arch; the unrecognized-arch path falls into the `unknown`
  # default branch.
  - install -D greeting.txt %{install-root}%{datadir}/greetings/%{arch-marker}.txt
