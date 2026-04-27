package main

import "testing"

func TestParsePlatform(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    [][2]string // pairs of (name, value)
		wantErr bool
	}{
		{name: "empty", in: "", want: nil},
		{name: "whitespace-only", in: "  \t ", want: nil},
		{
			name: "single",
			in:   "cmake-version=3.30.0",
			want: [][2]string{{"cmake-version", "3.30.0"}},
		},
		{
			name: "multi",
			in:   "OSFamily=linux,Arch=x86_64,cmake-version=3.30.0",
			want: [][2]string{
				{"OSFamily", "linux"},
				{"Arch", "x86_64"},
				{"cmake-version", "3.30.0"},
			},
		},
		{
			name: "trims whitespace",
			in:   "  Arch = x86_64 ,  OSFamily = linux  ",
			want: [][2]string{
				{"Arch", "x86_64"},
				{"OSFamily", "linux"},
			},
		},
		{name: "value with =", in: "container-image=docker://image=tag", want: [][2]string{{"container-image", "docker://image=tag"}}},
		{name: "missing =", in: "broken", wantErr: true},
		{name: "empty name", in: "=value", wantErr: true},
		{name: "empty value", in: "name=", want: [][2]string{{"name", ""}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePlatform(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr=%v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("len = %d, want %d (got=%v)", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i].Name != tc.want[i][0] || got[i].Value != tc.want[i][1] {
					t.Errorf("[%d] = (%q, %q), want (%q, %q)", i, got[i].Name, got[i].Value, tc.want[i][0], tc.want[i][1])
				}
			}
		})
	}
}
