kind: make

sources:
- kind: local
  path: sources

# No config: block — kind:make's defaults supply
#   build-commands:   ["make"]
#   install-commands: ["make -j1 DESTDIR=\"%{install-root}\" install"]
# which compiles greet.c via the Makefile's `all` target and then
# installs the resulting binary to %{install-root}%{prefix}/bin/greet.
