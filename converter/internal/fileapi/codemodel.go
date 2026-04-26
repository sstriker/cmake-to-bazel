package fileapi

// Codemodel is <reply>/codemodel-v2-*.json.
//
// Schema reference: cmake-file-api(7), "codemodel" object kind.
type Codemodel struct {
	Kind           string          `json:"kind"`
	Version        ObjectVersion   `json:"version"`
	Paths          CodemodelPaths  `json:"paths"`
	Configurations []Configuration `json:"configurations"`
}

// CodemodelPaths records source and build root absolute paths. The build path
// is non-deterministic across recordings (CMake creates per-invocation tmp
// dirs); avoid asserting on it in tests.
type CodemodelPaths struct {
	Source string `json:"source"`
	Build  string `json:"build"`
}

// Configuration is one build config (Release / Debug / etc.). Single-config
// generators (Ninja, Make) emit exactly one; multi-config (Xcode, MSVC) emit
// many.
type Configuration struct {
	Name        string            `json:"name"`
	Directories []ConfigDirectory `json:"directories"`
	Projects    []ConfigProject   `json:"projects"`
	Targets     []ConfigTargetRef `json:"targets"`
}

type ConfigDirectory struct {
	Source              string `json:"source"`
	Build               string `json:"build"`
	JSONFile            string `json:"jsonFile"`
	HasInstallRule      bool   `json:"hasInstallRule"`
	ProjectIndex        int    `json:"projectIndex"`
	TargetIndexes       []int  `json:"targetIndexes"`
	MinimumCMakeVersion struct {
		String string `json:"string"`
	} `json:"minimumCMakeVersion"`
}

type ConfigProject struct {
	Name             string `json:"name"`
	DirectoryIndexes []int  `json:"directoryIndexes"`
	TargetIndexes    []int  `json:"targetIndexes"`
}

// ConfigTargetRef is a per-config index entry pointing at a target's full JSON.
type ConfigTargetRef struct {
	Name           string `json:"name"`
	Id             string `json:"id"`
	JSONFile       string `json:"jsonFile"`
	DirectoryIndex int    `json:"directoryIndex"`
	ProjectIndex   int    `json:"projectIndex"`
}

// Target is <reply>/target-<name>-<config>-*.json.
//
// This is the principal input to the lowering stage: it carries the source
// list, per-language compile groups (with includes/defines/flags), link
// information, and install rules for one target in one configuration.
type Target struct {
	Name           string             `json:"name"`
	Id             string             `json:"id"`
	Type           string             `json:"type"`
	NameOnDisk     string             `json:"nameOnDisk"`
	Backtrace      int                `json:"backtrace"`
	Folder         TargetFolder       `json:"folder"`
	Paths          TargetPaths        `json:"paths"`
	Sources        []TargetSource     `json:"sources"`
	SourceGroups   []SourceGroup      `json:"sourceGroups"`
	CompileGroups  []CompileGroup     `json:"compileGroups"`
	Artifacts      []TargetArtifact   `json:"artifacts"`
	Archive        *TargetArchive     `json:"archive,omitempty"`
	Link           *TargetLink        `json:"link,omitempty"`
	Dependencies   []TargetDependency `json:"dependencies,omitempty"`
	Install        *TargetInstall     `json:"install,omitempty"`
	BacktraceGraph BacktraceGraph     `json:"backtraceGraph"`
}

type TargetFolder struct {
	Name string `json:"name"`
}

type TargetPaths struct {
	Source string `json:"source"`
	Build  string `json:"build"`
}

// TargetSource is one entry in target.sources[]. Path is relative to the
// project source root.
type TargetSource struct {
	Path              string `json:"path"`
	Backtrace         int    `json:"backtrace"`
	CompileGroupIndex int    `json:"compileGroupIndex"`
	SourceGroupIndex  int    `json:"sourceGroupIndex"`
	IsGenerated       bool   `json:"isGenerated"`
	FileSetIndex      *int   `json:"fileSetIndex,omitempty"`
}

type SourceGroup struct {
	Name          string `json:"name"`
	SourceIndexes []int  `json:"sourceIndexes"`
}

// CompileGroup aggregates sources sharing a language + flags + includes set.
type CompileGroup struct {
	Language                string             `json:"language"`
	SourceIndexes           []int              `json:"sourceIndexes"`
	CompileCommandFragments []CommandFragment  `json:"compileCommandFragments"`
	Includes                []CompileInclude   `json:"includes"`
	Defines                 []CompileDefine    `json:"defines"`
	Frameworks              []CompileFramework `json:"frameworks,omitempty"`
	PrecompileHeaders       []CompilePCH       `json:"precompileHeaders,omitempty"`
	LanguageStandard        *LanguageStandard  `json:"languageStandard,omitempty"`
}

// CommandFragment is one --flag or "-DFOO=bar" chunk passed to the compiler.
// Role is empty for compile fragments; for link fragments it's "flags",
// "libraries", "libraryPath", or "frameworkPath".
type CommandFragment struct {
	Fragment string `json:"fragment"`
	Role     string `json:"role,omitempty"`
}

type CompileInclude struct {
	Path      string `json:"path"`
	IsSystem  bool   `json:"isSystem,omitempty"`
	Backtrace int    `json:"backtrace,omitempty"`
}

type CompileDefine struct {
	Define    string `json:"define"`
	Backtrace int    `json:"backtrace,omitempty"`
}

type CompileFramework struct {
	Path     string `json:"path"`
	IsSystem bool   `json:"isSystem,omitempty"`
}

type CompilePCH struct {
	Header    string `json:"header"`
	Backtrace int    `json:"backtrace,omitempty"`
}

type LanguageStandard struct {
	Standard   string `json:"standard"`
	Backtraces []int  `json:"backtraces,omitempty"`
}

type TargetArtifact struct {
	Path string `json:"path"`
}

// TargetArchive is present for STATIC_LIBRARY targets.
type TargetArchive struct {
	CommandFragments []CommandFragment `json:"commandFragments,omitempty"`
	LTO              bool              `json:"lto,omitempty"`
}

// TargetLink is present for EXECUTABLE / SHARED_LIBRARY / MODULE_LIBRARY
// targets.
type TargetLink struct {
	Language         string            `json:"language"`
	CommandFragments []CommandFragment `json:"commandFragments,omitempty"`
	LTO              bool              `json:"lto,omitempty"`
	Sysroot          *struct {
		Path string `json:"path"`
	} `json:"sysroot,omitempty"`
}

type TargetDependency struct {
	Id        string `json:"id"`
	Backtrace int    `json:"backtrace,omitempty"`
}

// TargetInstall lists DESTINATION entries declared via install(TARGETS ...).
// Each destination's path is relative to install.prefix.
type TargetInstall struct {
	Prefix struct {
		Path string `json:"path"`
	} `json:"prefix"`
	Destinations []TargetInstallDest `json:"destinations"`
}

type TargetInstallDest struct {
	Path      string `json:"path"`
	Backtrace int    `json:"backtrace,omitempty"`
}

// BacktraceGraph is a deduplicated CST trace shared by all backtrace fields in
// a target file. Indices in the graph are referenced by the integer
// "backtrace" fields elsewhere in the same JSON object.
type BacktraceGraph struct {
	Commands []string        `json:"commands"`
	Files    []string        `json:"files"`
	Nodes    []BacktraceNode `json:"nodes"`
}

type BacktraceNode struct {
	File    int  `json:"file"`
	Line    int  `json:"line,omitempty"`
	Command int  `json:"command,omitempty"`
	Parent  *int `json:"parent,omitempty"`
}
