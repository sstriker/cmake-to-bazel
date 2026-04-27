"""Bzlmod extension exposing orchestrator-converted FDSDK elements as Bazel repos.

A downstream MODULE.bazel uses the extension like:

    converted = use_extension("@cmake_to_bazel//bazel:converted_pkg_repo.bzl", "converted_pkg_repo")
    converted.from_manifest(manifest = "//path/to:converted.json")
    use_repo(converted, "elem_components_hello", "elem_components_uses_hello")

Then `bazel build @elem_components_hello//:hello` works directly. The
imports-manifest the converter already emits uses these same repo
names, so cross-element deps round-trip transparently.

The element-name -> repo-name transform mirrors
orchestrator/internal/exports/extract.go's bazelRepoFor: prefix with
`elem_` and replace any non-[A-Za-z0-9_] character with `_`. Two
sources of truth would drift; instead we recompute here.
"""

_ALNUM = ("abcdefghijklmnopqrstuvwxyz" +
          "ABCDEFGHIJKLMNOPQRSTUVWXYZ" +
          "0123456789")

def _repo_name_for(element_name):
    """element name (with path components) -> Bazel repo identifier.

    Matches orchestrator/internal/exports/extract.go's bazelRepoFor:
    prefix `elem_`, replace any non-[A-Za-z0-9_] character with `_`.
    """
    out = []
    for i in range(len(element_name)):
        c = element_name[i]
        if c in _ALNUM or c == "_":
            out.append(c)
        else:
            out.append("_")
    return "elem_" + "".join(out)

def _converted_pkg_impl(rctx):
    """One repo per converted element. The element's output directory
    already contains a complete BUILD.bazel and a cmake-config/
    bundle directory; we symlink them in so downstream consumers see
    @<repo>//:<target> for Bazel deps and external/<repo>/cmake-config/
    for find_package consumers.
    """
    src = rctx.attr.path

    # The orchestrator's per-element output dir IS the repo root.
    rctx.symlink(src + "/BUILD.bazel", "BUILD.bazel")

    # cmake-config/ is optional — failure cases produce no bundle.
    bundle = src + "/cmake-config"
    if rctx.path(bundle).exists:
        rctx.symlink(bundle, "cmake-config")

    # MODULE.bazel + WORKSPACE.bazel mark this directory as a valid
    # Bazel repo root. The orchestrator output doesn't ship these
    # since it's not a repo on its own; we synthesize them.
    rctx.file("MODULE.bazel", "module(name = \"{}\")\n".format(rctx.name))
    rctx.file("WORKSPACE.bazel", "workspace(name = \"{}\")\n".format(rctx.name))

_converted_pkg_repository = repository_rule(
    implementation = _converted_pkg_impl,
    attrs = {
        "path": attr.string(
            mandatory = True,
            doc = "absolute path to the orchestrator's <out>/elements/<name>/ directory",
        ),
    },
    local = True,
)

def _from_manifest_impl(mctx):
    """Read each from_manifest tag, parse converted.json, declare one
    converted_pkg_repository per converted element.
    """
    for mod in mctx.modules:
        for tag in mod.tags.from_manifest:
            manifest_label = mctx.path(tag.manifest)
            body = mctx.read(manifest_label)
            doc = json.decode(body)
            if doc.get("version", 0) != 1:
                fail("converted_pkg_repo: unsupported manifest version {}".format(doc.get("version")))

            # Resolve <out>/elements/<name>/ from the manifest path.
            # converted.json lives at <out>/manifest/converted.json, so
            # element dirs are at <out>/elements/<name>/.
            out_root = manifest_label.dirname.dirname
            for elem in doc.get("elements", []):
                name = elem["name"]
                _converted_pkg_repository(
                    name = _repo_name_for(name),
                    path = "{}/elements/{}".format(str(out_root), name),
                )

_from_manifest = tag_class(
    attrs = {
        "manifest": attr.label(
            mandatory = True,
            doc = "label or path to the orchestrator's <out>/manifest/converted.json",
            allow_single_file = True,
        ),
    },
)

converted_pkg_repo = module_extension(
    implementation = _from_manifest_impl,
    tag_classes = {
        "from_manifest": _from_manifest,
    },
    doc = "Declare one Bazel repo per orchestrator-converted FDSDK element.",
)
