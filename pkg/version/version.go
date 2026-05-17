package version

var (
	Version = "dev"
	Date    = "unknown"
)

func Info() string {
	return Version + "-" + Date
}
