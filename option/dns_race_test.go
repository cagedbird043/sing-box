package option

import (
	C "github.com/sagernet/sing-box/constant"
	"strings"
	"testing"
)

func TestRaceServerValidation(t *testing.T) {
	race := func(tag string, servers ...string) DNSServerOptions {
		return DNSServerOptions{Type: C.DNSTypeRace, Tag: tag, Options: &RaceDNSServerOptions{Servers: servers}}
	}
	leaf := DNSServerOptions{Type: C.DNSTypeHTTPS, Tag: "leaf"}
	tests := []struct {
		name    string
		servers []DNSServerOptions
		want    string
	}{
		{"valid nested", []DNSServerOptions{race("a", "b"), race("b", "leaf"), leaf}, ""},
		{"empty", []DNSServerOptions{race("a")}, "must not be empty"},
		{"missing", []DNSServerOptions{race("a", "missing")}, "server[missing] not found"},
		{"self", []DNSServerOptions{race("a", "a")}, "self-reference"},
		{"cycle", []DNSServerOptions{race("a", "b"), race("b", "a")}, "a -> b -> a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options := DNSOptions{RawDNSOptions: RawDNSOptions{Servers: tt.servers}}
			err := options.ValidateRaceServers()
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %q, got %v", tt.want, err)
			}
		})
	}
}
