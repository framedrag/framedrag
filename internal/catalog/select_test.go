package catalog

import (
	"slices"
	"testing"
)

func names(feeds []Feed) []string {
	var out []string
	for _, f := range feeds {
		out = append(out, f.Name)
	}
	return out
}

func TestSelectByTier(t *testing.T) {
	c := mustLoad(t, "")
	// PRI1 spans both address families (PRI1 and PRI1_6 categories) and
	// excludes the discontinued Abuse_IPBL.
	want := []string{"Abuse_Feodo_C2", "Abuse_Feodo_C2_Agr", "CINS_army", "ET_Comp6"}
	if got := names(c.Select([]string{"PRI1"})); !slices.Equal(got, want) {
		t.Errorf("Select(PRI1) = %v, want %v", got, want)
	}
}

func TestSelectByCategory(t *testing.T) {
	c := mustLoad(t, "")
	if got := names(c.Select([]string{"TOR"})); !slices.Equal(got, []string{"Dan_me_TOR"}) {
		t.Errorf("Select(TOR) = %v", got)
	}
	// A category name narrower than its tier still works.
	if got := names(c.Select([]string{"PRI1_6"})); !slices.Equal(got, []string{"ET_Comp6"}) {
		t.Errorf("Select(PRI1_6) = %v", got)
	}
}

func TestSelectByFeedName(t *testing.T) {
	c := mustLoad(t, "")
	if got := names(c.Select([]string{"CINS_army"})); !slices.Equal(got, []string{"CINS_army"}) {
		t.Errorf("Select(CINS_army) = %v", got)
	}
}

func TestSelectMixedDedupes(t *testing.T) {
	c := mustLoad(t, "")
	// CINS_army matches both PRI1 and its own name; it must appear once.
	want := []string{"Abuse_Feodo_C2", "Abuse_Feodo_C2_Agr", "CINS_army", "Dan_me_TOR", "ET_Comp6"}
	got := names(c.Select([]string{"PRI1", "CINS_army", "TOR"}))
	if !slices.Equal(got, want) {
		t.Errorf("Select(mixed) = %v, want %v", got, want)
	}
}

func TestSelectSkipsDisabledAndKeyless(t *testing.T) {
	c := mustLoad(t, "")
	if got := c.Select([]string{"Abuse_IPBL"}); len(got) != 0 {
		t.Errorf("Select returned disabled feed: %v", names(got))
	}
	if got := c.Select([]string{"Pulsedive"}); len(got) != 0 {
		t.Errorf("Select returned RequiresKey feed: %v", names(got))
	}
	// Once the overlay supplies the key, the feed becomes selectable.
	c = mustLoad(t, "testdata/overlay.yaml")
	if got := names(c.Select([]string{"Pulsedive"})); !slices.Equal(got, []string{"Pulsedive"}) {
		t.Errorf("Select(Pulsedive) after key = %v", got)
	}
}

func TestSelectUnknownAndCase(t *testing.T) {
	c := mustLoad(t, "")
	if got := c.Select([]string{"NOPE", ""}); len(got) != 0 {
		t.Errorf("Select(unknown) = %v", names(got))
	}
	if got := names(c.Select([]string{"pri1_6"})); !slices.Equal(got, []string{"ET_Comp6"}) {
		t.Errorf("Select is not case-insensitive: %v", got)
	}
	if got := c.Select(nil); got != nil {
		t.Errorf("Select(nil) = %v", names(got))
	}
}
