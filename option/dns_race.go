package option

import (
	"strings"

	C "github.com/sagernet/sing-box/constant"
	E "github.com/sagernet/sing/common/exceptions"
)

// ValidateRaceServers validates references after all configuration files are merged.
func (o DNSOptions) ValidateRaceServers() error {
	servers := make(map[string]DNSServerOptions, len(o.Servers))
	for _, server := range o.Servers {
		servers[server.Tag] = server
	}
	state := make(map[string]uint8)
	var visit func(string, []string) error
	visit = func(tag string, path []string) error {
		if state[tag] == 1 {
			return E.New("circular race DNS server dependency: ", strings.Join(append(path, tag), " -> "))
		}
		if state[tag] == 2 {
			return nil
		}
		server := servers[tag]
		if server.Type != C.DNSTypeRace {
			return nil
		}
		options, ok := server.Options.(*RaceDNSServerOptions)
		if !ok {
			return E.New("race DNS server[", tag, "]: invalid options")
		}
		if len(options.Servers) == 0 {
			return E.New("race DNS server[", tag, "]: servers must not be empty")
		}
		state[tag] = 1
		for _, dependency := range options.Servers {
			if dependency == tag {
				return E.New("race DNS server[", tag, "]: self-reference is not allowed")
			}
			if _, exists := servers[dependency]; dependency == "" || !exists {
				return E.New("race DNS server[", tag, "]: server[", dependency, "] not found")
			}
			if err := visit(dependency, append(path, tag)); err != nil {
				return err
			}
		}
		state[tag] = 2
		return nil
	}
	for _, server := range o.Servers {
		if err := visit(server.Tag, nil); err != nil {
			return err
		}
	}
	return nil
}
