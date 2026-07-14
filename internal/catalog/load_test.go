package catalog

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const fixture = "testdata/feeds.json"

func mustLoad(t *testing.T, overlayPath string) Catalog {
	t.Helper()
	c, err := Load(fixture, overlayPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return c
}

func feedByName(t *testing.T, c Catalog, name string) Feed {
	t.Helper()
	for _, f := range c.Feeds {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("feed %q not found", name)
	return Feed{}
}

func TestLoadVendored(t *testing.T) {
	c := mustLoad(t, "")

	want := []string{
		"AWS_v4", "Abuse_Feodo_C2", "Abuse_Feodo_C2_Agr", "Abuse_IPBL",
		"CINS_army", "Dan_me_TOR", "ET_Comp6", "Pulsedive", "SFS_toxic6", "VPN6",
	}
	var got []string
	for _, f := range c.Feeds {
		got = append(got, f.Name)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("feed names = %v, want %v (sorted, dnsbl skipped)", got, want)
	}

	feodo := feedByName(t, c, "Abuse_Feodo_C2")
	if feodo.Category != "PRI1" || feodo.Tier != "PRI1" || feodo.IPVersion != "ipv4" {
		t.Errorf("Abuse_Feodo_C2 category/tier/ipversion = %q/%q/%q", feodo.Category, feodo.Tier, feodo.IPVersion)
	}
	if feodo.CadenceHours != 1 {
		t.Errorf("Abuse_Feodo_C2 cadence = %d, want 1", feodo.CadenceHours)
	}
	if feodo.Website != "https://feodotracker.abuse.ch/" {
		t.Errorf("Abuse_Feodo_C2 website = %q", feodo.Website)
	}

	alt := feedByName(t, c, "Abuse_Feodo_C2_Agr")
	if alt.URL != "https://feodotracker.abuse.ch/downloads/ipblocklist_aggressive.txt" {
		t.Errorf("alternate URL = %q", alt.URL)
	}
	if alt.Tier != "PRI1" || !strings.Contains(alt.Description, "May cause False positives") {
		t.Errorf("alternate tier/description = %q/%q", alt.Tier, alt.Description)
	}

	if f := feedByName(t, c, "Abuse_IPBL"); !f.Disabled {
		t.Error("discontinued feed Abuse_IPBL not Disabled")
	}
	if f := feedByName(t, c, "VPN6"); !f.Disabled {
		t.Error("feed in discontinued category VPN_6 not Disabled")
	}

	v6 := feedByName(t, c, "ET_Comp6")
	if v6.Tier != "PRI1" || v6.Category != "PRI1_6" || v6.IPVersion != "ipv6" {
		t.Errorf("ET_Comp6 tier/category/ipversion = %q/%q/%q, want PRI1/PRI1_6/ipv6", v6.Tier, v6.Category, v6.IPVersion)
	}

	if f := feedByName(t, c, "AWS_v4"); f.CadenceHours != 168 {
		t.Errorf("Weekly cadence = %d, want 168", f.CadenceHours)
	}
	if f := feedByName(t, c, "SFS_toxic6"); f.CadenceHours != 12 {
		t.Errorf("12hours cadence = %d, want 12", f.CadenceHours)
	}

	pd := feedByName(t, c, "Pulsedive")
	if !pd.RequiresKey {
		t.Error("Pulsedive with unfilled _API_KEY_ placeholder not marked RequiresKey")
	}
}

func TestLoadRealVendoredCatalog(t *testing.T) {
	// The genuine vendored upstream file must always parse. No network:
	// this reads the committed copy.
	c, err := Load(filepath.Join("..", "..", "catalog", "feeds.json"), "")
	if err != nil {
		t.Fatalf("Load real vendored catalog: %v", err)
	}
	if len(c.Feeds) < 100 {
		t.Fatalf("real catalog has %d feeds, expected 100+", len(c.Feeds))
	}
	if len(c.Select([]string{"PRI1"})) == 0 {
		t.Fatal("real catalog: Select(PRI1) is empty")
	}
}

func TestOverlayDisable(t *testing.T) {
	c := mustLoad(t, "testdata/overlay.yaml")
	if f := feedByName(t, c, "Dan_me_TOR"); !f.Disabled {
		t.Error("overlay-disabled feed Dan_me_TOR not Disabled")
	}
}

func TestOverlayAddFeed(t *testing.T) {
	c := mustLoad(t, "testdata/overlay.yaml")
	f := feedByName(t, c, "My_Local_Feed")
	if f.URL != "https://intel.example.net/badguys.txt" || f.Category != "LOCAL" || f.CadenceHours != 24 {
		t.Errorf("user feed = %+v", f)
	}
	// Result stays sorted after the append.
	if !slices.IsSortedFunc(c.Feeds, func(a, b Feed) int { return strings.Compare(a.Name, b.Name) }) {
		t.Error("Feeds not sorted by name after overlay")
	}
}

func TestOverlayAPIKey(t *testing.T) {
	c := mustLoad(t, "testdata/overlay.yaml")
	f := feedByName(t, c, "Pulsedive")
	if f.RequiresKey {
		t.Error("Pulsedive still RequiresKey after overlay supplied a key")
	}
	if !strings.Contains(f.URL, "sekrit-key-123") {
		t.Errorf("key not substituted into URL: %q", f.URL)
	}
	if strings.Contains(f.URL, APIKeyPlaceholder) {
		t.Errorf("placeholder still present: %q", f.URL)
	}
}

func TestRedaction(t *testing.T) {
	c := mustLoad(t, "testdata/overlay.yaml")
	f := feedByName(t, c, "Pulsedive")
	for name, s := range map[string]string{"String": f.String(), "RedactedURL": f.RedactedURL()} {
		if strings.Contains(s, "sekrit-key-123") {
			t.Errorf("%s leaks the api key: %q", name, s)
		}
	}
	if !strings.Contains(f.RedactedURL(), "_REDACTED_") {
		t.Errorf("RedactedURL has no redaction marker: %q", f.RedactedURL())
	}
	// Feeds without keys pass through untouched.
	plain := feedByName(t, c, "CINS_army")
	if plain.RedactedURL() != plain.URL {
		t.Errorf("RedactedURL mangled a keyless URL: %q", plain.RedactedURL())
	}
}

func writeOverlay(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "feeds.local.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOverlayErrors(t *testing.T) {
	cases := map[string]string{
		"disable unknown feed":  "disable: [No_Such_Feed]\n",
		"key for unknown feed":  "api_keys:\n  No_Such_Feed: abc\n",
		"key without a slot":    "api_keys:\n  CINS_army: abc\n",
		"empty key":             "api_keys:\n  Pulsedive: \"\"\n",
		"add without url":       "feeds:\n  - name: broken\n",
		"add colliding name":    "feeds:\n  - name: CINS_army\n    url: https://example.com/x\n",
		"unknown overlay field": "disble: [typo]\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(fixture, writeOverlay(t, body)); err == nil {
				t.Fatalf("Load accepted bad overlay %q", body)
			}
		})
	}
}

func TestOverlayErrorsNeverLeakKeys(t *testing.T) {
	// An error triggered by an api_keys entry must not echo the value.
	_, err := Load(fixture, writeOverlay(t, "api_keys:\n  No_Such_Feed: super-secret-value\n"))
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "super-secret-value") {
		t.Fatalf("error leaks api key: %v", err)
	}
}

func TestLoadMissingOverlayFileErrors(t *testing.T) {
	if _, err := Load(fixture, "testdata/does-not-exist.yaml"); err == nil {
		t.Fatal("Load accepted a nonexistent overlay path")
	}
}

func TestLoadRejectsNonCatalogJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feeds.json")
	if err := os.WriteFile(path, []byte(`{"hello": "world"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, ""); err == nil {
		t.Fatal("Load accepted JSON without ipv4/ipv6 sections")
	}
}
