kind: manual

sources:
- kind: local
  path: sources

config:
  # Trivial install-only pipeline: copy the staged greeting.txt into
  # %{install-root}%{prefix}/share/. write-a's manual handler maps
  # %{install-root} → an action-time mktemp dir and %{prefix} → /usr,
  # so the file lands at $INSTALL_ROOT/usr/share/greeting.txt and is
  # tarred as the element's install_tree.tar output.
  install-commands:
  - install -D greeting.txt %{install-root}%{prefix}/share/greeting.txt
