package version

// Installation paths
const (
	BinDir = "/usr/local/bin"
)

// GitHub repository
const (
	RepoOwner = "mordilloSan"
	RepoName  = "go-monitoring"
)

// Build info — set at build time via ldflags:
// go build -ldflags "-X github.com/mordilloSan/go-monitoring/internal/version.Version=v1.0.0"
var (
	Version   = "untracked"
	CommitSHA = ""
	BuildTime = ""
)
