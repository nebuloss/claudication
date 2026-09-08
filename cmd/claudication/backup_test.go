package main

import "testing"

func TestOneFileResolvesTheDestination(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flag  string
		rest  []string
		want  string
		fails bool
	}{
		// The bug: `backup /path/to.tar.gz` was ignored and the archive went
		// to a generated name in the working directory.
		{name: "positional only", rest: []string{"/tmp/b.tar.gz"}, want: "/tmp/b.tar.gz"},
		{name: "flag only", flag: "/tmp/b.tar.gz", want: "/tmp/b.tar.gz"},
		{name: "neither", want: ""},
		{name: "both, agreeing", flag: "/tmp/b.tar.gz", rest: []string{"/tmp/b.tar.gz"}, want: "/tmp/b.tar.gz"},

		{name: "both, disagreeing", flag: "/tmp/a.tar.gz", rest: []string{"/tmp/b.tar.gz"}, fails: true},
		{name: "two files", rest: []string{"a", "b"}, fails: true},
	} {
		got, err := oneFile("backup", "out", tc.flag, tc.rest)
		if tc.fails {
			if err == nil {
				t.Errorf("%s: got %q, want an error", tc.name, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
