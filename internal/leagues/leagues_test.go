package leagues

import "testing"

func TestLoadRegistry(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	for _, code := range []string{"IPL", "WPL", "BBL", "WBBL", "SA20"} {
		l, ok := r.Get(code)
		if !ok {
			t.Fatalf("existing league %s missing from leagues.json", code)
		}
		if !l.Visible {
			t.Errorf("existing league %s should stay visible", code)
		}
	}
}

func TestResolveSplitsMixedGenderSources(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	tests := []struct {
		event, gender, want string
	}{
		{"Super Smash", "male", "SSM"},
		{"Women's Super Smash", "female", "WSSM"},
		{"The Hundred Men's Competition", "male", "HND"},
		{"The Hundred Women's Competition", "female", "WHND"},
		{"Vitality Blast Men", "male", "NTB"},
		{"Vitality Blast Women", "female", "WTB"},
	}
	for _, tt := range tests {
		l, ok := r.Resolve(tt.event, tt.gender)
		if !ok || l.Code != tt.want {
			t.Errorf("Resolve(%q, %q) = %q, %v; want %q", tt.event, tt.gender, l.Code, ok, tt.want)
		}
	}

	if _, ok := r.Resolve("FairBreak Invitational Tournament", "female"); ok {
		t.Error("FairBreak is not an included league and must not resolve")
	}
	if _, ok := r.Resolve("Super Smash", "female"); ok {
		t.Error("an event must only resolve for its own gender")
	}
}

func TestSourcesFor(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	sources, err := r.SourcesFor([]string{"ssm", "WSSM", "IPL"})
	if err != nil {
		t.Fatalf("SourcesFor error: %v", err)
	}
	if len(sources) != 2 || sources[0] != "ssm_csv2" || sources[1] != "ipl_csv2" {
		t.Errorf("SourcesFor = %v, want [ssm_csv2 ipl_csv2]", sources)
	}

	if _, err := r.SourcesFor([]string{"NOPE"}); err == nil {
		t.Error("unknown league code should error")
	}
}
