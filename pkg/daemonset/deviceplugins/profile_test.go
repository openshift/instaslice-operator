package deviceplugins

import "testing"

func TestSanitizeProfileName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"1g.5gb+me", "1g.5gb-plus-me"},
		{"2g.10gb", "2g.10gb"},
		{"1g+2g+3g", "1g-plus-2g-plus-3g"},
	}
	for _, c := range cases {
		got := sanitizeProfileName(c.in)
		if got != c.want {
			t.Fatalf("sanitizeProfileName(%q)=%q, want %q", c.in, got, c.want)
		}
	}
}

func TestUnsanitizeProfileName(t *testing.T) {
	cases := []string{
		"1g.5gb+me",
		"2g.10gb",
		"1g+2g+3g",
	}
	for _, in := range cases {
		sanitized := sanitizeProfileName(in)
		got := unsanitizeProfileName(sanitized)
		if got != in {
			t.Fatalf("round-trip sanitize/unsanitize failed: got %q, want %q", got, in)
		}
	}
}
