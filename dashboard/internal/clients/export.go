package clients

import (
	"encoding/json"
	"fmt"
	"strings"

	"wireguard-dashboard/internal/db"
)

// ExportEntry is the narrow projection of a client that both export formats
// emit: the three fields Terraform's clients_config carries. The dashboard's
// runtime-only columns (enabled, note, timestamps) are intentionally dropped —
// they have no place in the Terraform seed, which only models who exists and at
// which address. Disabled clients ARE exported: the export is a reconciliation
// snapshot of every configured peer, and Terraform has no disabled concept.
//
// Exported (spec 017) so the canonical {name, address, public_key} projection
// is reusable outside this package — the bulk-replace endpoint's response body
// must equal this same shape (identical to GET /api/clients/export?format=tfvars)
// so a REST client's post-write state equals its subsequent read.
type ExportEntry struct {
	Name      string `json:"name"`
	Address   string `json:"address"`
	PublicKey string `json:"public_key"`
}

// ExportEntries projects a client slice down to the canonical
// {name, address, public_key} shape, in the same order as the input.
func ExportEntries(cs []db.Client) []ExportEntry {
	out := make([]ExportEntry, 0, len(cs))
	for _, c := range cs {
		out = append(out, ExportEntry{Name: c.Name, Address: c.Address, PublicKey: c.PublicKey})
	}
	return out
}

// ExportHCL renders the current clients as a paste-ready Terraform
// `clients_config = [...]` block matching the shape in terraform/dev/main.tf
// (object per client, fields aligned on '='). The output is pure and
// byte-stable for a given client slice, so it is trivially testable; the
// caller (the export handler) owns ordering by listing from the DB's stable
// address-then-id order.
//
// Every field value here is already validated to a safe charset (name charset,
// IPv4 /32 address, base64 public key), so no HCL string escaping is needed —
// none of the values can contain a quote or backslash.
func ExportHCL(cs []db.Client) string {
	var b strings.Builder
	b.WriteString("clients_config = [\n")
	for _, e := range ExportEntries(cs) {
		b.WriteString("  {\n")
		fmt.Fprintf(&b, "    %-10s = %q\n", "name", e.Name)
		fmt.Fprintf(&b, "    %-10s = %q\n", "address", e.Address)
		fmt.Fprintf(&b, "    %-10s = %q\n", "public_key", e.PublicKey)
		b.WriteString("  },\n")
	}
	b.WriteString("]\n")
	return b.String()
}

// ExportTFVars renders the current clients as the JSON body of a
// `clients.auto.tfvars.json` file: a single object with a `clients_config`
// array of {name, address, public_key}. Indented for human-readable git diffs.
// Returns valid JSON for any input (an empty slice yields an empty array, not
// null, because ExportEntries always allocates a non-nil slice).
func ExportTFVars(cs []db.Client) ([]byte, error) {
	doc := struct {
		ClientsConfig []ExportEntry `json:"clients_config"`
	}{ClientsConfig: ExportEntries(cs)}
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("clients: marshal tfvars export: %w", err)
	}
	return append(body, '\n'), nil
}
