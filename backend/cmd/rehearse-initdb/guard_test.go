package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureID = "0123456789abcdef0123456789abcdef"

func fixtureURL() string {
	q := url.Values{"host": {socketDirectory}, "sslmode": {"disable"}}
	return (&url.URL{Scheme: "postgresql", User: url.User(cloneUser), Path: "/gnomonitoring_rehearsal_" + fixtureID, RawQuery: q.Encode()}).String()
}

func TestCloneTargetHasNoDefaultOrTCPSpelling(t *testing.T) {
	canonical, err := canonicalCloneDSN(fixtureURL(), fixtureID)
	if err != nil || !strings.Contains(canonical, "TimeZone=UTC") || !strings.Contains(canonical, "statement_timeout=600000") {
		t.Fatal("explicit fixed clone target should be canonicalized")
	}
	for _, raw := range []string{
		"", "host=localhost dbname=gnomonitoring", "service=production",
		"postgresql://rehearsal_owner@localhost/gnomonitoring_rehearsal_" + fixtureID,
		"postgresql://rehearsal_owner@127.0.0.1/gnomonitoring_rehearsal_" + fixtureID,
		strings.Replace(fixtureURL(), "gnomonitoring_rehearsal_"+fixtureID, "gnomonitoring", 1),
		strings.Replace(fixtureURL(), cloneUser, "postgres", 1),
		fixtureURL() + "&service=production", fixtureURL() + "&options=-csearch_path%3Dproduction",
		fixtureURL() + "&host=%2Frun%2Fother", fixtureURL() + "&hostaddr=192.0.2.1",
		fixtureURL() + "&search_path=private", fixtureURL() + "#fragment",
		strings.Replace(fixtureURL(), "rehearsal-postgres", "rehearsal-postgres%2F..%2Fprod", 1),
	} {
		if _, err := canonicalCloneDSN(raw, fixtureID); err == nil {
			t.Fatal("unsafe or ambiguous clone connection was accepted")
		} else if strings.Contains(err.Error(), raw) && raw != "" {
			t.Fatal("connection rejection echoed its input")
		}
	}
	if _, err := canonicalCloneDSN(fixtureURL(), ""); err == nil {
		t.Fatal("missing clone marker ID was accepted")
	}
}

func TestPostgresEnvironmentCannotSupplyAnotherTarget(t *testing.T) {
	if err := rejectPostgresEnvironment([]string{"HOME=/tmp", "REHEARSAL_CLONE_ID=" + fixtureID}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"PGHOST", "PGHOSTADDR", "PGDATABASE", "PGSERVICE", "PGSERVICEFILE", "PGPASSWORD", "PGOPTIONS", "pgport"} {
		if rejectPostgresEnvironment([]string{key + "=redacted"}) == nil {
			t.Fatal("PostgreSQL environment fallback was accepted")
		}
	}
}

func TestNetworkFenceUsesKernelInterfacesAndRoutes(t *testing.T) {
	clean := networkEvidence{OS: "linux", UID: 999, Interfaces: []net.Interface{{Name: "lo", Flags: net.FlagLoopback | net.FlagUp}, {Name: "gre0"}},
		Addresses: map[string][]net.Addr{"lo": nil, "gre0": nil}, NoNewPrivs: true,
		IPv4Routes: "Iface Destination Gateway Flags RefCnt Use Metric Mask MTU Window IRTT\n"}
	if err := validateNetworkFence(clean); err != nil {
		t.Fatal(err)
	}
	for _, modify := range []func(*networkEvidence){
		func(s *networkEvidence) { s.OS = "darwin" },
		func(s *networkEvidence) { s.UID = 0 },
		func(s *networkEvidence) {
			s.Interfaces = append(s.Interfaces, net.Interface{Name: "eth0"})
			s.Addresses["eth0"] = nil
		},
		func(s *networkEvidence) { s.Interfaces[0].Flags = 0 },
		func(s *networkEvidence) { s.Interfaces[1].Flags = net.FlagUp },
		func(s *networkEvidence) {
			s.Addresses["gre0"] = []net.Addr{&net.IPNet{IP: net.ParseIP("192.0.2.1"), Mask: net.CIDRMask(32, 32)}}
		},
		func(s *networkEvidence) { s.CapEff = 1 },
		func(s *networkEvidence) { s.CapPrm = 1 },
		func(s *networkEvidence) { s.NoNewPrivs = false },
		func(s *networkEvidence) { s.IPv4Routes += "eth0 00000000 01000000 0003 0 0 0 00000000 0 0 0\n" },
		func(s *networkEvidence) { s.IPv4Routes += "gre0 00000000 01000000 0003 0 0 0 00000000 0 0 0\n" },
		func(s *networkEvidence) { s.IPv6Routes = "0 0 0 0 0 0 0 0 0 eth0\n" },
		func(s *networkEvidence) { s.IPv4Routes = "unreadable" },
	} {
		state := clean
		state.Interfaces = append([]net.Interface(nil), clean.Interfaces...)
		state.Addresses = map[string][]net.Addr{"lo": nil, "gre0": nil}
		modify(&state)
		if validateNetworkFence(state) == nil {
			t.Fatal("network or privilege escape was accepted")
		}
	}
}

func TestMarkerOwnersAreIndependentEvenIfOwnerRevokesItsOwnACL(t *testing.T) {
	valid := markerOwnership{SchemaOwner: "rehearsal_admin", TableOwner: "rehearsal_admin", RelationKind: "r"}
	if validateMarkerOwnership(valid) != nil {
		t.Fatal("independent bootstrap owners should be accepted")
	}
	for _, change := range []func(*markerOwnership){
		func(m *markerOwnership) { m.SchemaOwner = cloneUser },
		func(m *markerOwnership) { m.TableOwner = cloneUser },
		func(m *markerOwnership) { m.SchemaRoleReachable = true },
		func(m *markerOwnership) { m.TableRoleReachable = true },
		func(m *markerOwnership) { m.RelationKind = "v" },
	} {
		candidate := valid
		change(&candidate)
		if validateMarkerOwnership(candidate) == nil {
			t.Fatal("marker owner or owner-role membership was accepted")
		}
	}
}

func TestPrivateDSNFileRefusesSymlinksAndSharedPermissions(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "clone.dsn")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := secureFile(path, os.Geteuid()); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(directory, "alias")
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := secureFile(alias, os.Geteuid()); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := secureFile(path, os.Geteuid()); err == nil {
		t.Fatal("shared credential file accepted")
	}
}

func TestRefusedWithoutExplicitMutatingOptIn(t *testing.T) {
	for _, args := range [][]string{nil, {"--clone-id", fixtureID}, {"--dsn", "redacted"}, {"--allow-initdb-on-isolated-clone"}} {
		report, code := run(args)
		if code != 1 || report.Status != "refused" || report.ErrorClass == "" || strings.Contains(report.ErrorClass, "redacted") {
			t.Fatal("missing opt-in did not fail closed")
		}
	}
}

func TestPrivateCaptureCoversExistingStdoutAndStderrLoggers(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "private-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	beforeStdout := os.Stdout
	err = capturePrivate(file, func() error {
		_, _ = fmt.Fprintln(beforeStdout, "synthetic-private-sql-detail")
		_, _ = fmt.Fprintln(os.Stderr, "synthetic-private-connection-detail")
		return errors.New("safe_failure_class")
	})
	if err == nil || err.Error() != "safe_failure_class" {
		t.Fatal("private capture changed the operation result")
	}
	data, err := os.ReadFile(file.Name())
	if err != nil || !strings.Contains(string(data), "synthetic-private-sql-detail") || !strings.Contains(string(data), "synthetic-private-connection-detail") {
		t.Fatal("descriptor-level capture missed a pre-existing logger")
	}
}

func TestAnyDeletedRowsRequireExplicitReview(t *testing.T) {
	removed := removedRows(map[string]tableCount{"existing": {Present: true, Rows: 2}, "grown": {Rows: 1}}, map[string]tableCount{"existing": {Present: true, Rows: 1}, "grown": {Rows: 3}})
	if len(removed) != 1 || removed["existing"] != 1 {
		t.Fatal("row loss was not measured")
	}
}
