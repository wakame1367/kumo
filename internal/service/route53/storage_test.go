package route53

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const (
	testZoneID        = "/hostedzone/test"
	testZoneName      = "example.com."
	testActionDelete  = "DELETE"
	testActionUpsert  = "UPSERT"
	testCreatedRecord = "www.example.com. 192.0.2.1"
	testUpdatedRecord = "www.example.com. 192.0.2.2"
)

func newTestRecordSet(name, value string) ResourceRecordSet {
	ttl := int64(300)

	return ResourceRecordSet{
		Name:            name,
		Type:            "A",
		TTL:             &ttl,
		ResourceRecords: []ResourceRecord{{Value: value}},
	}
}

// recordSummaries returns "name value" for every record set in the zone.
func recordSummaries(t *testing.T, s *MemoryStorage) []string {
	t.Helper()

	records, err := s.GetRecordSets(testZoneID)
	if err != nil {
		t.Fatal(err)
	}

	summaries := make([]string, 0, len(records))
	for i := range records {
		summaries = append(summaries, records[i].Name+" "+records[i].ResourceRecords[0].Value)
	}

	return summaries
}

var recordNameNormalizationTests = []struct {
	name       string
	createName string
	change     Change
	want       []string
}{
	{
		name:       "delete with trailing dot",
		createName: "www.example.com",
		change:     Change{Action: testActionDelete, ResourceRecordSet: newTestRecordSet("www.example.com.", "192.0.2.1")},
		want:       []string{},
	},
	{
		name:       "delete without trailing dot",
		createName: "www.example.com.",
		change:     Change{Action: testActionDelete, ResourceRecordSet: newTestRecordSet("www.example.com", "192.0.2.1")},
		want:       []string{},
	},
	{
		name:       "upsert with trailing dot",
		createName: "www.example.com",
		change:     Change{Action: testActionUpsert, ResourceRecordSet: newTestRecordSet("www.example.com.", "192.0.2.2")},
		want:       []string{testUpdatedRecord},
	},
	{
		name:       "upsert without trailing dot",
		createName: "www.example.com.",
		change:     Change{Action: testActionUpsert, ResourceRecordSet: newTestRecordSet("www.example.com", "192.0.2.2")},
		want:       []string{testUpdatedRecord},
	},
	{
		name:       "delete in another case",
		createName: "WWW.Example.com",
		change:     Change{Action: testActionDelete, ResourceRecordSet: newTestRecordSet("www.example.com.", "192.0.2.1")},
		want:       []string{},
	},
	{
		name:       "upsert in another case",
		createName: "www.Example.COM.",
		change:     Change{Action: testActionUpsert, ResourceRecordSet: newTestRecordSet("WWW.EXAMPLE.COM", "192.0.2.2")},
		want:       []string{testUpdatedRecord},
	},
}

func TestChangeRecordSetsNormalizesRecordNames(t *testing.T) {
	t.Parallel()

	for _, tt := range recordNameNormalizationTests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := NewMemoryStorage()
			if err := s.CreateHostedZone(&HostedZone{ID: testZoneID, Name: testZoneName}); err != nil {
				t.Fatal(err)
			}

			create := Change{Action: "CREATE", ResourceRecordSet: newTestRecordSet(tt.createName, "192.0.2.1")}
			if err := s.ChangeRecordSets(testZoneID, []Change{create}); err != nil {
				t.Fatalf("CREATE: %v", err)
			}

			if got, want := recordSummaries(t, s), []string{testCreatedRecord}; !slices.Equal(got, want) {
				t.Fatalf("records after CREATE = %q, want %q", got, want)
			}

			if err := s.ChangeRecordSets(testZoneID, []Change{create}); !errors.Is(err, ErrRecordSetAlreadyExists) {
				t.Fatalf("duplicate CREATE error = %v, want %v", err, ErrRecordSetAlreadyExists)
			}

			if err := s.ChangeRecordSets(testZoneID, []Change{tt.change}); err != nil {
				t.Fatalf("%s: %v", tt.change.Action, err)
			}

			if got := recordSummaries(t, s); !slices.Equal(got, tt.want) {
				t.Fatalf("records after %s = %q, want %q", tt.change.Action, got, tt.want)
			}
		})
	}
}

// loadSnapshot writes a snapshot holding records as an earlier version
// stored them, with names kept exactly as sent, and loads it.
func loadSnapshot(t *testing.T, records []ResourceRecordSet) *MemoryStorage {
	t.Helper()

	legacy := NewMemoryStorage()
	legacy.HostedZones[testZoneID] = &HostedZone{ID: testZoneID, Name: testZoneName, ResourceRecordSetCount: int64(len(records))}
	legacy.RecordSets[testZoneID] = records

	return writeAndLoad(t, legacy)
}

// writeAndLoad writes the state of legacy as a snapshot and loads it into a
// new MemoryStorage.
func writeAndLoad(t *testing.T, legacy *MemoryStorage) *MemoryStorage {
	t.Helper()

	data, err := legacy.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "route53.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	return NewMemoryStorage(WithDataDir(dataDir))
}

var loadNormalizationTests = []struct {
	name            string
	persisted       []ResourceRecordSet
	wantLoaded      []string
	wantAfterDelete []string
}{
	{
		name:            "undotted name",
		persisted:       []ResourceRecordSet{newTestRecordSet("www.example.com", "192.0.2.1")},
		wantLoaded:      []string{testCreatedRecord},
		wantAfterDelete: []string{},
	},
	{
		name: "both spellings with identical values",
		persisted: []ResourceRecordSet{
			newTestRecordSet("www.example.com", "192.0.2.1"),
			newTestRecordSet("www.example.com.", "192.0.2.1"),
		},
		wantLoaded:      []string{testCreatedRecord},
		wantAfterDelete: []string{},
	},
	{
		name: "both spellings with different values keep the later one",
		persisted: []ResourceRecordSet{
			newTestRecordSet("www.example.com", "192.0.2.1"),
			newTestRecordSet("api.example.com.", "192.0.2.10"),
			newTestRecordSet("www.example.com.", "192.0.2.9"),
		},
		wantLoaded:      []string{"api.example.com. 192.0.2.10", "www.example.com. 192.0.2.9"},
		wantAfterDelete: []string{"api.example.com. 192.0.2.10"},
	},
	{
		name:            "upper-case name",
		persisted:       []ResourceRecordSet{newTestRecordSet("WWW.EXAMPLE.COM.", "192.0.2.1")},
		wantLoaded:      []string{testCreatedRecord},
		wantAfterDelete: []string{},
	},
	{
		name: "spellings that differ in case keep the later one",
		persisted: []ResourceRecordSet{
			newTestRecordSet("WWW.Example.com", "192.0.2.1"),
			newTestRecordSet("www.example.com.", "192.0.2.9"),
		},
		wantLoaded:      []string{"www.example.com. 192.0.2.9"},
		wantAfterDelete: []string{},
	},
}

func TestLoadNormalizesPersistedRecordNames(t *testing.T) {
	t.Parallel()

	for _, tt := range loadNormalizationTests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := loadSnapshot(t, tt.persisted)

			if got := recordSummaries(t, s); !slices.Equal(got, tt.wantLoaded) {
				t.Fatalf("records after load = %q, want %q", got, tt.wantLoaded)
			}

			zone, err := s.GetHostedZone(testZoneID)
			if err != nil {
				t.Fatal(err)
			}

			if zone.ResourceRecordSetCount != int64(len(tt.wantLoaded)) {
				t.Fatalf("ResourceRecordSetCount = %d, want %d", zone.ResourceRecordSetCount, len(tt.wantLoaded))
			}

			upsert := Change{Action: testActionUpsert, ResourceRecordSet: newTestRecordSet("www.example.com.", "192.0.2.2")}
			if err := s.ChangeRecordSets(testZoneID, []Change{upsert}); err != nil {
				t.Fatalf("UPSERT: %v", err)
			}

			wantUpserted := slices.Concat(tt.wantAfterDelete, []string{testUpdatedRecord})
			if got := recordSummaries(t, s); !slices.Equal(got, wantUpserted) {
				t.Fatalf("records after UPSERT = %q, want %q", got, wantUpserted)
			}

			del := Change{Action: testActionDelete, ResourceRecordSet: newTestRecordSet("www.example.com", "192.0.2.2")}
			if err := s.ChangeRecordSets(testZoneID, []Change{del}); err != nil {
				t.Fatalf("DELETE: %v", err)
			}

			if got := recordSummaries(t, s); !slices.Equal(got, tt.wantAfterDelete) {
				t.Fatalf("records after DELETE = %q, want %q", got, tt.wantAfterDelete)
			}
		})
	}
}

func TestLoadDropsRecordSetsOfMissingZone(t *testing.T) {
	t.Parallel()

	const missingZoneID = "/hostedzone/missing"

	legacy := NewMemoryStorage()
	legacy.HostedZones[testZoneID] = &HostedZone{ID: testZoneID, Name: testZoneName, ResourceRecordSetCount: 1}
	legacy.RecordSets[testZoneID] = []ResourceRecordSet{newTestRecordSet("www.example.com.", "192.0.2.1")}
	legacy.RecordSets[missingZoneID] = []ResourceRecordSet{newTestRecordSet("www.missing.example.", "192.0.2.2")}

	s := writeAndLoad(t, legacy)

	if _, ok := s.RecordSets[missingZoneID]; ok {
		t.Fatalf("record sets of %s were kept after load", missingZoneID)
	}

	if got, want := recordSummaries(t, s), []string{testCreatedRecord}; !slices.Equal(got, want) {
		t.Fatalf("records after load = %q, want %q", got, want)
	}
}

func TestCoalesceRecordSetsLogsDroppedRecordSet(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer

	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	dropped := newTestRecordSet("www.example.com.", "192.0.2.1")
	kept := newTestRecordSet("www.example.com.", "192.0.2.9")

	got := coalesceRecordSets(logger, testZoneID, []ResourceRecordSet{dropped, kept, kept})
	if !reflect.DeepEqual(got, []ResourceRecordSet{kept}) {
		t.Fatalf("coalesced = %+v, want only the later record set", got)
	}

	// The identical copy of the kept record set is dropped without a log; the
	// differing earlier one is logged in a form that restores it.
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("logged %d lines, want 1:\n%s", len(lines), logs.String())
	}

	var entry struct {
		Dropped string `json:"dropped"`
	}

	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatal(err)
	}

	var restored ResourceRecordSet
	if err := json.Unmarshal([]byte(entry.Dropped), &restored); err != nil {
		t.Fatalf("logged record set %q: %v", entry.Dropped, err)
	}

	if !reflect.DeepEqual(restored, dropped) {
		t.Fatalf("logged record set = %+v, want %+v", restored, dropped)
	}
}
