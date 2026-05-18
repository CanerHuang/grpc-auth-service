// Package version exposes build-time metadata. All three vars are populated
// by `-ldflags -X` in build.sh; the defaults below are what `go run` / IDE
// builds will show.
package version

var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// Info renders the short form used in startup log: "<version>-<date>".
// Commit is exposed separately via the gRPC VersionInfo message.
func Info() string {
	return Version + "-" + Date
}
