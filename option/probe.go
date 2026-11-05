package option

import "github.com/sagernet/sing/common/json/badoption"

type NodeProbeServiceOptions struct {
	Targets        badoption.Listable[string] `json:"targets,omitempty"`
	DownloadBytes  uint32                     `json:"download_bytes,omitempty"`
	Timeout        badoption.Duration         `json:"timeout,omitempty"`
	MaxRTTRatio    float64                    `json:"max_rtt_ratio,omitempty"`
	ScreenInterval badoption.Duration         `json:"screen_interval,omitempty"`
	CheckInterval  badoption.Duration         `json:"check_interval,omitempty"`
	DomesticURL    string                     `json:"domestic_url,omitempty"`
}

type ProbeOutboundOptions struct {
	GroupCommonOption
	Probe                     string `json:"probe,omitempty"`
	URL                       string `json:"url,omitempty"`
	PreferOrder               bool   `json:"prefer_order,omitempty"`
	InterruptExistConnections bool   `json:"interrupt_exist_connections,omitempty"`
}
