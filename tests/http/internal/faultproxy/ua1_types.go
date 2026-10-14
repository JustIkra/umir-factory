package faultproxy

type UA1Identity struct {
	FixtureID       string `json:"fixtureID"`
	InstanceID      string `json:"instanceID"`
	ClockGeneration string `json:"clockGeneration"`
	BinarySHA256    string `json:"binarySHA256"`
	ConfigSHA256    string `json:"configSHA256"`
}

type UA1LinkSelector struct {
	JobID        string `json:"jobID"`
	InvitationID string `json:"invitationID"`
}

type UA1LinkCapture struct {
	CaptureID          string          `json:"captureID"`
	Sequence           uint64          `json:"sequence"`
	Selector           UA1LinkSelector `json:"selector"`
	MessageID          string          `json:"messageID"`
	Accepted           bool            `json:"accepted"`
	Reply              string          `json:"reply"`
	Bytes              int             `json:"bytes"`
	ContentSHA256      string          `json:"contentSHA256"`
	Owner              UA1Identity     `json:"owner"`
	OrgID              string          `json:"orgID"`
	AggregateRevision  string          `json:"aggregateRevision"`
	DiscoveryRequestID string          `json:"discoveryRequestID"`
	DiscoverySHA256    string          `json:"discoverySHA256"`
}

type UA1LinkObservation struct {
	Captures []UA1LinkCapture `json:"captures"`
	Complete bool             `json:"complete"`
	Dropped  int              `json:"dropped"`
}

type UA1FaultObservation struct {
	FaultID string `json:"faultID"`
	State   string `json:"state"`
	Hits    int    `json:"hits"`
}

type ua1DiscoveryResponse struct {
	BindingSHA256     string      `json:"bindingSHA256"`
	Project           string      `json:"project"`
	RequestID         string      `json:"requestID"`
	Identity          UA1Identity `json:"identity"`
	Instant           string      `json:"instant"`
	AggregateRevision string      `json:"aggregateRevision"`
	Selector          struct {
		Kind         string `json:"kind"`
		OrgID        string `json:"orgID"`
		InvitationID string `json:"invitationID"`
		JobID        string `json:"jobID"`
	} `json:"selector"`
	Jobs []struct {
		Kind      string `json:"kind"`
		JobID     string `json:"jobID"`
		MessageID string `json:"messageID"`
		ExpiresAt string `json:"expiresAt"`
		Anchor    struct {
			OrgID        string `json:"orgID"`
			InvitationID string `json:"invitationID"`
			ChallengeID  string `json:"challengeID"`
		} `json:"anchor"`
	} `json:"jobs"`
}
