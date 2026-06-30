package clients

import (
	"encoding/json"
	"regexp"
	"testing"

	"wireguard-dashboard/internal/db"
)

// hclEntryRe extracts the three fields of each clients_config object from the
// HCL export. The export is deterministic and uses %q-quoted values, so a
// per-field regex is a faithful parse without pulling in an HCL dependency.
var hclEntryRe = regexp.MustCompile(`(?s)\{\s*name\s*=\s*"([^"]*)"\s*address\s*=\s*"([^"]*)"\s*public_key\s*=\s*"([^"]*)"\s*\}`)

func sampleClients() []db.Client {
	return []db.Client{
		{Name: "alice", Address: "172.16.15.6/32", PublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="},
		{Name: "bob", Address: "172.16.15.7/32", PublicKey: "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB="},
	}
}

func TestExportHCL_ParsesToEntries(t *testing.T) {
	out := ExportHCL(sampleClients())

	matches := hclEntryRe.FindAllStringSubmatch(out, -1)
	if len(matches) != 2 {
		t.Fatalf("parsed %d HCL entries, want 2\n--- output ---\n%s", len(matches), out)
	}

	want := sampleClients()
	for i, m := range matches {
		if m[1] != want[i].Name || m[2] != want[i].Address || m[3] != want[i].PublicKey {
			t.Errorf("entry %d = {name=%q address=%q public_key=%q}, want {%q %q %q}",
				i, m[1], m[2], m[3], want[i].Name, want[i].Address, want[i].PublicKey)
		}
	}

	// Paste-ready shape: the assignment header must be present.
	if !regexp.MustCompile(`^clients_config = \[`).MatchString(out) {
		t.Errorf("HCL export missing `clients_config = [` header:\n%s", out)
	}
}

func TestExportHCL_Empty(t *testing.T) {
	out := ExportHCL(nil)
	if out != "clients_config = [\n]\n" {
		t.Errorf("empty HCL export = %q, want an empty block", out)
	}
}

func TestExportTFVars_ValidJSON(t *testing.T) {
	body, err := ExportTFVars(sampleClients())
	if err != nil {
		t.Fatalf("ExportTFVars: %v", err)
	}

	var doc struct {
		ClientsConfig []struct {
			Name      string `json:"name"`
			Address   string `json:"address"`
			PublicKey string `json:"public_key"`
		} `json:"clients_config"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("tfvars export is not valid JSON: %v\n--- output ---\n%s", err, body)
	}
	if len(doc.ClientsConfig) != 2 {
		t.Fatalf("tfvars export has %d entries, want 2", len(doc.ClientsConfig))
	}
	want := sampleClients()
	for i, e := range doc.ClientsConfig {
		if e.Name != want[i].Name || e.Address != want[i].Address || e.PublicKey != want[i].PublicKey {
			t.Errorf("entry %d = %+v, want {%q %q %q}", i, e, want[i].Name, want[i].Address, want[i].PublicKey)
		}
	}
}

func TestExportTFVars_EmptyArrayNotNull(t *testing.T) {
	body, err := ExportTFVars(nil)
	if err != nil {
		t.Fatalf("ExportTFVars(nil): %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("empty tfvars export is not valid JSON: %v", err)
	}
	if string(doc["clients_config"]) != "[]" {
		t.Errorf("empty clients_config = %s, want []", doc["clients_config"])
	}
}
