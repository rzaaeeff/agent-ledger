package ledger

import "testing"

func TestLooksSimilar(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"don't touch the migrations, schema frozen until Friday", "don't touch migrations until Monday", true},
		{"don't touch migrations until Friday", "Don't touch migrations until Friday!", true},
		{"use pnpm workspaces", "use pnpm workspaces in CI", true},
		{"don't touch migrations", "run migrations before the tests", false},
		{"use pnpm", "use bun", false},
		{"deploy from release branch", "never push to main branch", false},
	} {
		if got := looksSimilar(c.a, c.b); got != c.want {
			t.Errorf("looksSimilar(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestPinMatches(t *testing.T) {
	p := Pin{ID: "a1b2c3", Text: "Don't touch the migrations, schema frozen until Friday"}
	for q, want := range map[string]bool{
		"a1b2c3": true, "[A1B2C3]": true, "touch the migrations": true, "MIGRATIONS, schema": true,
		"touch migrations friday": true, "friday migrations": true, "migr": true,
		"touch migrations monday": false, "": false, "  ": false, "!!": false,
	} {
		if got := pinMatches(p, q); got != want {
			t.Errorf("pinMatches(%q) = %v", q, got)
		}
	}
}
