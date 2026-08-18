package tui

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

func pickRunPort(host string, preferred int, occupied map[int]bool) (int, error) {
	if preferred <= 0 {
		preferred = 8000
	}
	for port := preferred; port < preferred+100; port++ {
		if occupied[port] {
			continue
		}
		if portAvailable(host, port) {
			return port, nil
		}
	}
	return 0, fmt.Errorf("no available port found near %d", preferred)
}

func portAvailable(host string, port int) bool {
	h := strings.TrimSpace(host)
	if h == "" {
		h = "127.0.0.1"
	}
	if h == "0.0.0.0" || h == "::" {
		h = ""
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(h, strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}
