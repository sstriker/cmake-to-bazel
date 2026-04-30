kind: script

sources:
- kind: local
  path: sources

config:
  # kind:script's flat command list — equivalent to kind:manual's
  # install-commands but without the configure / build / strip
  # phase scaffolding. write-a maps this onto the pipelineHandler's
  # install-commands slot internally.
  commands:
  - mkdir -p %{install-root}%{datadir}/scripts
  - install -D -m 0644 greeting.txt %{install-root}%{datadir}/scripts/hello.txt
