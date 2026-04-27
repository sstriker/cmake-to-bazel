kind: cmake

description: |
  hello-world equivalent in the orchestrator fixture: a single cmake
  element with one runtime dep on base.

depends:
- base.bst

sources:
- kind: local
  path: ../../../../converter/testdata/sample-projects/hello-world
