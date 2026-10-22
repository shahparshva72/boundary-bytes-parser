package database

import "testing"

func TestSlugify(t *testing.T) {
	tests := map[string]string{
		"V Kohli":            "v-kohli",
		"Emma Jones":         "emma-jones",
		"  AB de Villiers":   "ab-de-villiers",
		"Mohammad Nawaz (3)": "mohammad-nawaz-3",
		"Ömer":               "mer",
		"!!!":                "abc123",
	}
	for name, want := range tests {
		if got := slugify(name, "ABC123"); got != want {
			t.Errorf("slugify(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestNextFreeSlug(t *testing.T) {
	taken := map[string]bool{"e-jones": true, "e-jones-2": true}
	if got := nextFreeSlug("e-jones", taken); got != "e-jones-3" {
		t.Errorf("nextFreeSlug = %q, want e-jones-3", got)
	}
	if got := nextFreeSlug("emma-jones", taken); got != "emma-jones" {
		t.Errorf("nextFreeSlug = %q, want emma-jones", got)
	}
}
