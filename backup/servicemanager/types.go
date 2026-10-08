package servicemanager

// Service manager type constants
const (
	TypeDockerCompose = "docker-compose"
	TypePodmanCompose = "podman-compose"
	TypeSystemd       = "systemd"
	TypeNoop          = "noop"
)

// Systemd scope constants
const (
	ScopeSystem = "system" // systemctl commands
	ScopeUser   = "user"   // systemctl --user commands
)

// Default systemd scope
const DefaultSystemdScope = ScopeSystem

// Binary names for each service manager type
const (
	BinaryDocker    = "docker"
	BinaryPodman    = "podman"
	BinarySystemctl = "systemctl"
)

// Service manager display names for logging
type DisplayNames struct {
	DockerCompose string
	PodmanCompose string
	Systemd       string
	System        string
	User          string
}

var Display = DisplayNames{
	DockerCompose: "Docker Compose",
	PodmanCompose: "Podman Compose",
	Systemd:       "Systemd",
	System:        "system",
	User:          "user",
}
