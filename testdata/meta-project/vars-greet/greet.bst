kind: manual

sources:
- kind: local
  path: sources

# Element-level variables: layer on top of the project defaults baked
# into write-a (variables.go's projectVars). prefix is overridden so
# every derived path (%{datadir}, %{libdir}, ...) shifts off /usr,
# and a fresh user-defined variable %{greeting-dir} composes onto the
# overridden derivation chain.
variables:
  prefix: /opt/freedesktop-sdk
  greeting-dir: "%{datadir}/greetings"

config:
  install-commands:
  # Resolves to:
  #   install -D greeting.txt $INSTALL_ROOT/opt/freedesktop-sdk/share/greetings/hello.txt
  # under the variable resolver: greeting-dir → %{datadir}/greetings,
  # datadir → %{prefix}/share, prefix overridden to /opt/freedesktop-sdk.
  - install -D greeting.txt %{install-root}%{greeting-dir}/hello.txt
