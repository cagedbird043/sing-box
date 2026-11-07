package option

import "github.com/sagernet/sing/common/json/badoption"

type NodeProbeServiceOptions struct {
	Domestic      *NodeProbeDomesticOptions          `json:"domestic,omitempty"`
	TrafficWindow badoption.Duration                 `json:"traffic_window,omitempty"`
	Measures      map[string]NodeProbeMeasureOptions `json:"measures,omitempty"`
	Policies      map[string]NodeProbePolicyOptions  `json:"policies,omitempty"`
}

type NodeProbeDomesticOptions struct {
	URL     string             `json:"url,omitempty"`
	Timeout badoption.Duration `json:"timeout,omitempty"`
	Bytes   uint32             `json:"bytes,omitempty"`
}

type NodeProbeMeasureOptions struct {
	Kind        string                     `json:"kind,omitempty"`
	Targets     badoption.Listable[string] `json:"targets,omitempty"`
	TargetMode  string                     `json:"target_mode,omitempty"`
	Bytes       uint32                     `json:"bytes,omitempty"`
	Timeout     badoption.Duration         `json:"timeout,omitempty"`
	Samples     int                        `json:"samples,omitempty"`
	Concurrency int                        `json:"concurrency,omitempty"`
	Window      int                        `json:"window,omitempty"`
}

type NodeProbePolicyOptions struct {
	Screen   *NodeProbeScreenOptions `json:"screen,omitempty"`
	Check    *NodeProbeCheckOptions  `json:"check,omitempty"`
	Eligible string                  `json:"eligible,omitempty"`
	Qualify  string                  `json:"qualify,omitempty"`
	Score    string                  `json:"score,omitempty"`
	Switch   string                  `json:"switch,omitempty"`
	Penalty  string                  `json:"penalty,omitempty"`
}

type NodeProbeScreenOptions struct {
	Measures        badoption.Listable[string] `json:"measures,omitempty"`
	Interval        badoption.Duration         `json:"interval,omitempty"`
	OnNetworkChange bool                       `json:"on_network_change,omitempty"`
}

type NodeProbeCheckOptions struct {
	Measures         badoption.Listable[string] `json:"measures,omitempty"`
	Interval         badoption.Duration         `json:"interval,omitempty"`
	OnTrafficFailure bool                       `json:"on_traffic_failure,omitempty"`
	Cooldown         badoption.Duration         `json:"cooldown,omitempty"`
}

type ProbeOutboundOptions struct {
	GroupCommonOption
	Probe                     string `json:"probe,omitempty"`
	Policy                    string `json:"policy,omitempty"`
	InterruptExistConnections bool   `json:"interrupt_exist_connections,omitempty"`
}
