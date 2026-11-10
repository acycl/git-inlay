package gitx

import (
	"slices"
	"testing"
)

// parseVersion reads the versions git prints, the vendors' forms among
// them, and refuses what is not one.
func TestParseVersion(t *testing.T) {
	for out, want := range map[string][2]int{
		"git version 2.54.0":                  {2, 54},
		"git version 2.40.1":                  {2, 40},
		"git version 2.47.1.windows.1":        {2, 47},
		"git version 2.39.5 (Apple Git-154)":  {2, 39},
		"git version 2.56.0.rc1.12.gabcdef0":  {2, 56},
		"git version 3.0":                     {3, 0},
		"git version 2.54.0\nextra line here": {2, 54},
	} {
		major, minor, err := parseVersion(out)
		if err != nil || major != want[0] || minor != want[1] {
			t.Errorf("parseVersion(%q) = %d, %d, %v; want %d, %d", out, major, minor, err, want[0], want[1])
		}
	}
	for _, out := range []string{"", "git version", "git version 2", "git version two.one", "git version 2.x.1"} {
		if major, minor, err := parseVersion(out); err == nil {
			t.Errorf("parseVersion(%q) = %d, %d, want an error", out, major, minor)
		}
	}
}

// The runner hands git the caller's environment without the variables that
// would point it at another repository, which a hook's environment holds,
// and with its own additions last, so that they win.
func TestEnvironScrubsRepositoryVariables(t *testing.T) {
	r := Runner{
		Env:      []string{"HOME=/h", "GIT_DIR=/elsewhere", "GIT_WORK_TREE=/elsewhere", "GIT_INDEX_FILE=/elsewhere/index", "GIT_COMMON_DIR=/elsewhere", "GIT_AUTHOR_NAME=a"},
		ExtraEnv: []string{"GIT_INDEX_FILE=/mine"},
	}
	got := r.environ()
	for _, kv := range []string{"GIT_DIR=/elsewhere", "GIT_WORK_TREE=/elsewhere", "GIT_INDEX_FILE=/elsewhere/index", "GIT_COMMON_DIR=/elsewhere"} {
		if slices.Contains(got, kv) {
			t.Errorf("environ kept %s", kv)
		}
	}
	for _, kv := range []string{"HOME=/h", "GIT_AUTHOR_NAME=a"} {
		if !slices.Contains(got, kv) {
			t.Errorf("environ dropped %s", kv)
		}
	}
	if got[len(got)-1] != "GIT_INDEX_FILE=/mine" {
		t.Errorf("environ ends with %q, want the runner's own variable", got[len(got)-1])
	}
}
