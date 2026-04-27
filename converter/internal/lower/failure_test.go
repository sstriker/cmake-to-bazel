package lower_test

import (
	"errors"
	"testing"

	"github.com/sstriker/cmake-to-bazel/converter/internal/failure"
	"github.com/sstriker/cmake-to-bazel/converter/internal/fileapi"
	"github.com/sstriker/cmake-to-bazel/converter/internal/lower"
)

// Surface tests: each Tier-1 code emitted by lower has at least one synthetic
// reply that triggers it. Codes documented in docs/failure-schema.md must be
// either exercised here or marked (M2)/reserved in the doc.

func TestFailure_UnsupportedTargetType(t *testing.T) {
	// OBJECT_LIBRARY isn't lowered yet; UTILITY (M2) is silently skipped
	// since the underlying add_custom_command is recovered separately, so
	// we use OBJECT_LIBRARY here to exercise the unsupported-target-type
	// emission point.
	r := &fileapi.Reply{
		Codemodel: fileapi.Codemodel{
			Configurations: []fileapi.Configuration{{
				Name:    "Release",
				Targets: []fileapi.ConfigTargetRef{{Name: "obj", Id: "obj::@1"}},
			}},
		},
		Targets: map[string]fileapi.Target{
			"obj::@1": {
				Name: "obj",
				Type: "OBJECT_LIBRARY",
			},
		},
	}
	_, err := lower.ToIR(r, nil, lower.Options{})
	assertCode(t, err, failure.UnsupportedTargetType)
}

func TestFailure_UnsupportedCustomCommand_GeneratedSource(t *testing.T) {
	r := &fileapi.Reply{
		Codemodel: fileapi.Codemodel{
			Configurations: []fileapi.Configuration{{
				Name:    "Release",
				Targets: []fileapi.ConfigTargetRef{{Name: "lib", Id: "lib::@1"}},
			}},
		},
		Targets: map[string]fileapi.Target{
			"lib::@1": {
				Name: "lib",
				Type: "STATIC_LIBRARY",
				Sources: []fileapi.TargetSource{{
					Path:        "generated.c",
					IsGenerated: true,
				}},
			},
		},
	}
	_, err := lower.ToIR(r, nil, lower.Options{})
	assertCode(t, err, failure.UnsupportedCustomCommand)
}

func TestFailure_FileAPIMalformed_DanglingTargetRef(t *testing.T) {
	r := &fileapi.Reply{
		Codemodel: fileapi.Codemodel{
			Configurations: []fileapi.Configuration{{
				Name: "Release",
				Targets: []fileapi.ConfigTargetRef{{
					Name: "ghost", Id: "ghost::@nonexistent",
				}},
			}},
		},
		Targets: map[string]fileapi.Target{}, // ref not present
	}
	_, err := lower.ToIR(r, nil, lower.Options{})
	assertCode(t, err, failure.FileAPIMalformed)
}

func TestFailure_UnsupportedTargetType_MultiConfig(t *testing.T) {
	// M1 supports exactly one configuration. Codemodel with two trips the
	// blanket reject in lower.ToIR. Doc lists this under
	// `unsupported-target-type` until M2 adds multi-config support.
	r := &fileapi.Reply{
		Codemodel: fileapi.Codemodel{
			Configurations: []fileapi.Configuration{
				{Name: "Release"},
				{Name: "Debug"},
			},
		},
	}
	_, err := lower.ToIR(r, nil, lower.Options{})
	assertCode(t, err, failure.UnsupportedTargetType)
}

func assertCode(t *testing.T, err error, want failure.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected Tier-1 error with code %q, got nil", want)
	}
	var fe *failure.Error
	if !errors.As(err, &fe) {
		t.Fatalf("err = %v (%T), want *failure.Error", err, err)
	}
	if fe.Code != want {
		t.Errorf("code = %q, want %q", fe.Code, want)
	}
}
