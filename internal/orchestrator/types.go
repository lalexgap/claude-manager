package orchestrator

type Status struct {
	DBPath      string
	Initialized bool
	AgentCount  int
	EventCount  int
}

type Agent struct {
	ID            int64
	Name          string
	WorkspacePath string
	Status        string
	SessionID     string
	LastHeartbeat string
	CreatedAt     string
	UpdatedAt     string
}
