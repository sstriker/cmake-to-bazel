// gen-scale-fixture writes orchestrator/testdata/fdsdk-scale/, a 50-element
// synthetic kind:cmake graph the orchestrator's concurrency tests drive at
// c=1/8/32. Run from repo root:
//
//	go run ./tools/fixtures/gen-scale-fixture
//
// Idempotent: regenerating the same fixture produces byte-identical output.
//
// Graph shape (50 elements, four levels, deterministic dep edges):
//
//	level 0 (leaves):       10 elements, no deps                  L00..L09
//	level 1:                20 elements, each deps on 2 leaves    L10..L29
//	level 2:                15 elements, each deps on 2 level-1   L30..L44
//	level 3:                 5 elements, each deps on 3 level-2   L45..L49
//
// Picks dep targets by index-modulo arithmetic so the dep graph is
// reproducible without RNG.
package main

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	numLeaves   = 10
	numLevel1   = 20
	numLevel2   = 15
	numLevel3   = 5
	totalLevels = numLeaves + numLevel1 + numLevel2 + numLevel3
)

func main() {
	if totalLevels != 50 {
		panic(fmt.Sprintf("totalLevels=%d, want 50", totalLevels))
	}
	root := "orchestrator/testdata/fdsdk-scale"
	if err := os.MkdirAll(filepath.Join(root, "elements"), 0o755); err != nil {
		die(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "files"), 0o755); err != nil {
		die(err)
	}

	for i := 0; i < totalLevels; i++ {
		name := elementName(i)
		writeElementBst(root, i, name)
		writeFiles(root, name, i)
	}
	writeReadme(root)
	fmt.Fprintf(os.Stderr, "wrote %d elements to %s\n", totalLevels, root)
}

func elementName(i int) string {
	return fmt.Sprintf("L%02d", i)
}

// deps returns the names of elements i depends on. Indices are picked via
// modulo arithmetic so the dep graph is deterministic without RNG.
func deps(i int) []string {
	switch {
	case i < numLeaves:
		return nil
	case i < numLeaves+numLevel1:
		// Each level-1 element depends on 2 leaves picked by (i mod 10) and (i+1 mod 10).
		j := i - numLeaves
		return []string{elementName(j % numLeaves), elementName((j + 1) % numLeaves)}
	case i < numLeaves+numLevel1+numLevel2:
		// Each level-2 element depends on 2 level-1 elements.
		j := i - numLeaves - numLevel1
		return []string{
			elementName(numLeaves + (j % numLevel1)),
			elementName(numLeaves + ((j + 3) % numLevel1)),
		}
	default:
		// Each level-3 element depends on 3 level-2 elements.
		j := i - numLeaves - numLevel1 - numLevel2
		return []string{
			elementName(numLeaves + numLevel1 + (j % numLevel2)),
			elementName(numLeaves + numLevel1 + ((j + 5) % numLevel2)),
			elementName(numLeaves + numLevel1 + ((j + 7) % numLevel2)),
		}
	}
}

func writeElementBst(root string, i int, name string) {
	var body string
	body = "kind: cmake\n"
	body += "\n"
	body += "description: |\n"
	body += "  Synthetic fdsdk-scale element " + name + ".\n"
	body += "\n"
	if d := deps(i); len(d) > 0 {
		body += "depends:\n"
		for _, dep := range d {
			body += "- " + dep + ".bst\n"
		}
		body += "\n"
	}
	body += "sources:\n"
	body += "- kind: local\n"
	body += "  path: ../files/" + name + "\n"
	dst := filepath.Join(root, "elements", name+".bst")
	if err := os.WriteFile(dst, []byte(body), 0o644); err != nil {
		die(err)
	}
}

func writeFiles(root, name string, i int) {
	dir := filepath.Join(root, "files", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		die(err)
	}

	cmake := "cmake_minimum_required(VERSION 3.20)\n" +
		"project(" + name + " LANGUAGES C VERSION 0.1.0)\n\n"
	for _, dep := range deps(i) {
		cmake += "find_package(" + dep + " CONFIG REQUIRED)\n"
	}
	cmake += "\nadd_library(" + name + " STATIC src/" + name + ".c)\n"
	cmake += "target_include_directories(" + name + " PUBLIC\n"
	cmake += "    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>\n"
	cmake += "    $<INSTALL_INTERFACE:include>)\n"
	if d := deps(i); len(d) > 0 {
		cmake += "target_link_libraries(" + name + " PUBLIC"
		for _, dep := range d {
			cmake += " " + dep + "::" + dep
		}
		cmake += ")\n"
	}
	cmake += "\ninstall(TARGETS " + name + "\n"
	cmake += "        EXPORT " + name + "Targets\n"
	cmake += "        ARCHIVE DESTINATION lib)\n"
	cmake += "install(EXPORT " + name + "Targets\n"
	cmake += "        FILE " + name + "Targets.cmake\n"
	cmake += "        NAMESPACE " + name + "::\n"
	cmake += "        DESTINATION lib/cmake/" + name + ")\n"
	if err := os.WriteFile(filepath.Join(dir, "CMakeLists.txt"), []byte(cmake), 0o644); err != nil {
		die(err)
	}

	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		die(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "include"), 0o755); err != nil {
		die(err)
	}

	c := "// Synthetic source for fdsdk-scale element " + name + ".\n"
	c += "int " + name + "_value(void) { return " + fmt.Sprintf("%d", i) + "; }\n"
	if err := os.WriteFile(filepath.Join(dir, "src", name+".c"), []byte(c), 0o644); err != nil {
		die(err)
	}
	h := "#pragma once\nint " + name + "_value(void);\n"
	if err := os.WriteFile(filepath.Join(dir, "include", name+".h"), []byte(h), 0o644); err != nil {
		die(err)
	}
}

func writeReadme(root string) {
	body := "fdsdk-scale: 50-element synthetic kind:cmake graph for orchestrator concurrency tests.\n\n" +
		"Layered: 10 leaves (no deps), 20 level-1 (2 leaves each), 15 level-2 (2 level-1 each), 5 level-3 (3 level-2 each).\n\n" +
		"Regenerate with: go run ./tools/fixtures/gen-scale-fixture\n"
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(body), 0o644); err != nil {
		die(err)
	}
}

func die(err error) {
	fmt.Fprintf(os.Stderr, "gen-scale-fixture: %v\n", err)
	os.Exit(1)
}
