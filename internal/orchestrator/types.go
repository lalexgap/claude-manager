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

type MainEnvRequest struct {
	ID          int64
	AgentID     int64
	AgentName   string
	RequestType string
	PayloadJSON string
	Reason      string
	Priority    string
	Status      string
	QueuedAt    string
	StartedAt   string
	FinishedAt  string
	ResultJSON  string
}

type LeaseInfo struct {
	HolderAgentID   int64
	HolderAgentName string
	Mode            string
	LeaseToken      string
	AcquiredAt      string
	ExpiresAt       string
	LastHeartbeatAt string
	Active          bool
}

type MainEnvStatus struct {
	Lease      LeaseInfo
	QueueDepth int
}
