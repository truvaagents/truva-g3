package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/truvaagents/truva-g3/core"
)

func serviceURL(service *core.ServiceInfo, endpoint string) string {
	address := strings.TrimSpace(service.Address)
	if !strings.Contains(address, "://") {
		address = "http://" + address
	}
	parsed, err := url.Parse(address)
	if err == nil && parsed.Host != "" {
		if parsed.Port() == "" && service.Port > 0 {
			parsed.Host = fmt.Sprintf("%s:%d", parsed.Hostname(), service.Port)
		}
		parsed.Path = endpoint
		parsed.RawQuery = ""
		parsed.Fragment = ""
		return parsed.String()
	}
	return fmt.Sprintf("http://%s:%d%s", service.Address, service.Port, endpoint)
}
