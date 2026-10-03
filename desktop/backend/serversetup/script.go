// Package serversetup provisions a VPS over SSH by running the same
// server-control script the Android app ships (see server-control/src/*.sh,
// vendored here verbatim from turn-proxy-android). The script speaks a small
// JSON protocol (see 10-proto.sh): one line of {"result":"ok","data":{...}}
// or {"result":"err","code":...} per invocation.
package serversetup

import (
	"embed"
	"sort"
	"strings"
)

//go:embed server-control/src/*.sh
var scriptFS embed.FS

var controlScript = assembleScript()

// assembleScript concatenates the numbered script fragments in filename order,
// mirroring turn-proxy-android's Gradle `assembleControlScript` task exactly
// (sorted join, each fragment trimmed of its trailing newline).
func assembleScript() string {
	entries, err := scriptFS.ReadDir("server-control/src")
	if err != nil {
		panic("serversetup: " + err.Error())
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	parts := make([]string, 0, len(names))
	for _, n := range names {
		b, err := scriptFS.ReadFile("server-control/src/" + n)
		if err != nil {
			panic("serversetup: " + err.Error())
		}
		parts = append(parts, strings.TrimRight(string(b), "\n"))
	}
	return strings.Join(parts, "\n") + "\n"
}
