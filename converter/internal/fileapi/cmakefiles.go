package fileapi

// CMakeFiles is <reply>/cmakeFiles-v1-*.json.
//
// Lists every file consumed during configure: CMakeLists, included .cmake
// modules (project-internal and bundled), generated configure-time outputs.
// We use this to drive shadow-tree allowlist augmentation (M3) and to compute
// the "configure inputs" content fingerprint (M3 cache key).
//
// Schema reference: cmake-file-api(7), "cmakeFiles" object kind.
type CMakeFiles struct {
	Kind    string         `json:"kind"`
	Version ObjectVersion  `json:"version"`
	Paths   CMakeFilePaths `json:"paths"`
	Inputs  []CMakeFileIn  `json:"inputs"`
}

type CMakeFilePaths struct {
	Source string `json:"source"`
	Build  string `json:"build"`
}

// CMakeFileIn flags whether each consumed file is a generated artifact, an
// external file (outside the source root, e.g. CMake bundled modules), or a
// .cmake-language script.
type CMakeFileIn struct {
	Path        string `json:"path"`
	IsGenerated bool   `json:"isGenerated,omitempty"`
	IsExternal  bool   `json:"isExternal,omitempty"`
	IsCMake     bool   `json:"isCMake,omitempty"`
}
