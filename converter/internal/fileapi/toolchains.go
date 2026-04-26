package fileapi

// Toolchains is <reply>/toolchains-v1-*.json.
//
// Schema reference: cmake-file-api(7), "toolchains" object kind.
type Toolchains struct {
	Kind       string         `json:"kind"`
	Version    ObjectVersion  `json:"version"`
	Toolchains []ToolchainEnt `json:"toolchains"`
}

// ToolchainEnt is one per-language compiler description.
type ToolchainEnt struct {
	Language             string            `json:"language"`
	SourceFileExtensions []string          `json:"sourceFileExtensions"`
	Compiler             ToolchainCompiler `json:"compiler"`
}

type ToolchainCompiler struct {
	Id       string            `json:"id"`
	Path     string            `json:"path"`
	Version  string            `json:"version"`
	Target   string            `json:"target,omitempty"`
	Implicit ToolchainImplicit `json:"implicit"`
}

// ToolchainImplicit are the directories and libraries the compiler adds
// without being asked. Bazel needs these to construct cc_toolchain
// configuration; we use them to filter out implicit -I/-L from generated
// BUILD files (don't redeclare what the toolchain provides).
type ToolchainImplicit struct {
	IncludeDirectories       []string `json:"includeDirectories"`
	LinkDirectories          []string `json:"linkDirectories"`
	LinkFrameworkDirectories []string `json:"linkFrameworkDirectories"`
	LinkLibraries            []string `json:"linkLibraries"`
}
