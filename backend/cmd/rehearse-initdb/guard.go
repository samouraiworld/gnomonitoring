package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5"
)

const (
	socketDirectory = "/run/rehearsal-postgres"
	dsnFilePath     = socketDirectory + "/clone.dsn"
	privateLogDir   = "/run/rehearsal-private"
	cloneUser       = "rehearsal_owner"
	baseRevision    = "92ed63fe7eb5ec54327aff36dc2bcaf2e6f24968"
)

var cloneIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type networkEvidence struct {
	OS         string
	UID        int
	Interfaces []net.Interface
	IPv4Routes string
	IPv6Routes string
	Addresses  map[string][]net.Addr
	CapEff     uint64
	CapPrm     uint64
	NoNewPrivs bool
}

func readNetworkEvidence() (networkEvidence, error) {
	state := networkEvidence{OS: runtime.GOOS, UID: os.Geteuid()}
	if state.OS != "linux" {
		return state, errors.New("linux_network_fence_required")
	}
	var err error
	state.Interfaces, err = net.Interfaces()
	if err != nil {
		return state, errors.New("network_fence_unreadable")
	}
	state.Addresses = make(map[string][]net.Addr)
	for _, device := range state.Interfaces {
		addresses, err := device.Addrs()
		if err != nil {
			return state, errors.New("network_fence_unreadable")
		}
		state.Addresses[device.Name] = addresses
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return state, errors.New("process_fence_unreadable")
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(status), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		switch fields[0] {
		case "CapEff:", "CapPrm:":
			value, err := strconv.ParseUint(fields[1], 16, 64)
			if err != nil {
				return state, errors.New("process_fence_unreadable")
			}
			if fields[0] == "CapEff:" {
				state.CapEff = value
			} else {
				state.CapPrm = value
			}
			seen[fields[0]] = true
		case "NoNewPrivs:":
			state.NoNewPrivs = fields[1] == "1"
			seen[fields[0]] = true
		}
	}
	if len(seen) != 3 {
		return state, errors.New("process_fence_unreadable")
	}
	v4, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return state, errors.New("network_fence_unreadable")
	}
	v6, err := os.ReadFile("/proc/net/ipv6_route")
	if err != nil {
		return state, errors.New("network_fence_unreadable")
	}
	state.IPv4Routes, state.IPv6Routes = string(v4), string(v6)
	return state, nil
}

func validateNetworkFence(state networkEvidence) error {
	if state.OS != "linux" || state.UID != 999 {
		return errors.New("isolated_linux_uid999_required")
	}
	if state.CapEff != 0 || state.CapPrm != 0 || !state.NoNewPrivs {
		return errors.New("zero_capabilities_and_no_new_privileges_required")
	}
	loopbacks := 0
	// Linux can install these fallback tunnel devices in every namespace,
	// including Docker's network:none. They are permitted only DOWN with no
	// addresses and no routes, while capabilities cannot bring them up.
	dormant := map[string]bool{"tunl0": true, "sit0": true, "gre0": true, "gretap0": true, "erspan0": true,
		"ip6tnl0": true, "ip_vti0": true, "ip6_vti0": true, "ip6gre0": true}
	for _, device := range state.Interfaces {
		addresses, ok := state.Addresses[device.Name]
		if !ok {
			return errors.New("network_fence_unreadable")
		}
		if device.Name == "lo" && device.Flags&net.FlagLoopback != 0 && device.Flags&net.FlagUp != 0 {
			loopbacks++
			for _, address := range addresses {
				ip, _, err := net.ParseCIDR(address.String())
				if err != nil || !ip.IsLoopback() {
					return errors.New("non_loopback_address_refused")
				}
			}
		} else if !dormant[device.Name] || device.Flags&net.FlagUp != 0 || len(addresses) != 0 {
			return errors.New("active_or_unknown_network_interface_refused")
		}
	}
	if loopbacks != 1 {
		return errors.New("loopback_interface_flag_required")
	}
	lines := strings.Split(strings.TrimSpace(state.IPv4Routes), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "Iface") {
		return errors.New("network_fence_unreadable")
	}
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) != 0 && (len(fields) < 11 || fields[0] != "lo") {
			return errors.New("external_route_refused")
		}
	}
	for _, line := range strings.Split(state.IPv6Routes, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 0 && (len(fields) != 10 || fields[9] != "lo") {
			return errors.New("external_route_refused")
		}
	}
	return nil
}

func rejectPostgresEnvironment(environment []string) error {
	for _, item := range environment {
		key, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(strings.ToUpper(key), "PG") {
			return errors.New("postgres_environment_fallback_refused")
		}
	}
	return nil
}

func secureFile(path string, uid int) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Mode().Perm()&0400 == 0 || info.Size() > 16*1024 {
		return nil, errors.New("private_regular_dsn_file_required")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != uid {
		return nil, errors.New("dsn_file_owner_mismatch")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("private_dsn_file_unreadable")
	}
	return data, nil
}

// Only a URL spelling of the fixed Unix socket is accepted. Reconstructing the
// connection URL prevents pgx/GORM parsing a different target or options later.
func canonicalCloneDSN(raw, cloneID string) (string, error) {
	if !cloneIDPattern.MatchString(cloneID) {
		return "", errors.New("explicit_clone_id_required")
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "postgresql" && u.Scheme != "postgres") || u.Host != "" || u.Opaque != "" || u.Fragment != "" || u.User == nil {
		return "", errors.New("explicit_unix_socket_clone_url_required")
	}
	databaseName := "gnomonitoring_rehearsal_" + cloneID
	if u.User.Username() != cloneUser || u.Path != "/"+databaseName {
		return "", errors.New("clone_database_and_role_mismatch")
	}
	values, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", errors.New("clone_url_options_refused")
	}
	allowed := map[string]string{
		"host": socketDirectory, "sslmode": "disable", "connect_timeout": "5",
		"TimeZone": "UTC", "search_path": "public",
	}
	for key, values := range values {
		expected, ok := allowed[key]
		if !ok || len(values) != 1 || values[0] != expected {
			return "", errors.New("clone_url_options_refused")
		}
	}
	if values.Get("host") != socketDirectory || values.Get("sslmode") != "disable" {
		return "", errors.New("fixed_clone_socket_required")
	}
	q := url.Values{
		"host": {socketDirectory}, "sslmode": {"disable"}, "connect_timeout": {"5"},
		"TimeZone": {"UTC"}, "search_path": {"public"},
		"application_name":  {"gnomonitoring-initdb-rehearsal"},
		"statement_timeout": {"600000"}, "lock_timeout": {"5000"},
	}
	canonical := (&url.URL{Scheme: "postgresql", User: u.User, Path: "/" + databaseName, RawQuery: q.Encode()}).String()
	config, err := pgx.ParseConfig(canonical)
	if err != nil || config.Host != socketDirectory || config.Database != databaseName || config.User != cloneUser || len(config.Fallbacks) != 0 || config.TLSConfig != nil {
		return "", errors.New("clone_connection_target_refused")
	}
	return canonical, nil
}

func validateFilesystemFence() error {
	for _, directory := range []string{socketDirectory, privateLogDir} {
		resolved, err := filepath.EvalSymlinks(directory)
		if err != nil || resolved != directory {
			return errors.New("fixed_clone_mounts_required")
		}
	}
	info, err := os.Stat(privateLogDir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("private_log_directory_required")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 999 {
		return errors.New("private_log_directory_owner_mismatch")
	}
	socket, err := os.Lstat(fmt.Sprintf("%s/.s.PGSQL.5432", socketDirectory))
	if err != nil || socket.Mode()&os.ModeSocket == 0 {
		return errors.New("local_clone_socket_required")
	}
	return nil
}
