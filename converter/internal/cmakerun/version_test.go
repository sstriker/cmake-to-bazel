package cmakerun

import "testing"

func TestParseCMakeVersion(t *testing.T) {
	cases := []struct {
		in                  string
		major, minor, patch int
		ok                  bool
		desc                string
	}{
		{"cmake version 3.28.3\nCMake suite ...", 3, 28, 3, true, "ubuntu 24.04 default"},
		{"cmake version 3.31.6\nCMake suite ...", 3, 31, 6, true, "GH runner toolcache"},
		{"cmake version 3.20.0", 3, 20, 0, true, "minimum supported"},
		{"cmake version 3.19.7", 3, 19, 7, true, "below floor — parses anyway"},
		{"cmake version 4.0.0", 4, 0, 0, true, "future major"},
		{"not cmake output", 0, 0, 0, false, "unparseable"},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			major, minor, patch, ok := parseCMakeVersion(c.in)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if !ok {
				return
			}
			if major != c.major || minor != c.minor || patch != c.patch {
				t.Errorf("got %d.%d.%d, want %d.%d.%d",
					major, minor, patch, c.major, c.minor, c.patch)
			}
		})
	}
}
