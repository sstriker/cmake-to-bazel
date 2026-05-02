kind: filter

depends:
- greet

config:
  # Domain names this filter intends to keep. Until BuildStream's
  # public-data parser lands and the parent's public: domains: block
  # is read, the filter element renders as a pass-through filegroup
  # over its single dep, and these domain names are recorded as a
  # comment inside the rendered BUILD.bazel for documentation.
  include:
  - public-headers
