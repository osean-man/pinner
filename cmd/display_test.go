package cmd

import (
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
	"github.com/osean-man/pinner/internal/database"
)

func TestDisplayCommandFitsWidth(t *testing.T) {
	long := `curl -X POST "https://example.com/search" -H "Content-Type: application/json" -d '{"email": "a@b.c"}'`
	cases := []struct {
		name    string
		command string
		width   int
	}{
		{"long ascii", long, 40},
		{"wide runes", strings.Repeat("日本語", 30), 25},
		{"tabs and newlines", "for i in 1 2 3;\n\tdo echo $i;\ndone", 20},
		{"tiny width", long, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := displayCommand(c.command, c.width)
			if w := runewidth.StringWidth(got); w > c.width {
				t.Errorf("width %d exceeds max %d: %q", w, c.width, got)
			}
			if strings.ContainsAny(got, "\r\n\t") {
				t.Errorf("contains control whitespace: %q", got)
			}
		})
	}
}

func TestDisplayCommandShortUnchanged(t *testing.T) {
	if got := displayCommand("ls -la", 80); got != "ls -la" {
		t.Errorf("got %q, want %q", got, "ls -la")
	}
}

func TestSensitivePinsNeverShowTheirArguments(t *testing.T) {
	secret := "kdfsaj-NOT-A-REAL-KEY-0001"
	p := database.Pin{Command: `curl -H "X-API-KEY: ` + secret + `" https://api.example.com`, Sensitive: true}
	for _, text := range []string{pinText(p), pinItems([]database.Pin{p})[0].Display} {
		if strings.Contains(text, secret) || strings.Contains(text, "api.example.com") {
			t.Fatalf("sensitive pin leaked: %q", text)
		}
		if !strings.Contains(text, "curl") {
			t.Fatalf("masked text should keep the program name: %q", text)
		}
	}
	p.Label = "prod search"
	if got := pinText(p); !strings.HasPrefix(got, "[prod search] ") || strings.Contains(got, secret) {
		t.Fatalf("labelled sensitive pin: %q", got)
	}
}

func TestSearchMatchesLabelTagsAndCommand(t *testing.T) {
	items := pinItems([]database.Pin{
		{ID: 1, Command: "kubectl logs -l app=worker -f", Label: "worker logs", Tags: []string{"k8s", "prod"}},
		{ID: 2, Command: "ssh -L 5601:localhost:5601 host", Label: "kibana tunnel", Sensitive: true},
	})
	cases := map[string][]int64{
		"worker":        {1},
		"PROD":          {1},
		"k8s logs":      {1},
		"tunnel":        {2},
		"5601":          {2}, // sensitive commands are still searchable, just not shown
		"kibana prod":   nil,
		"   ":           {1, 2},
		"localhost ssh": {2},
	}
	for input, want := range cases {
		var got []int64
		for _, it := range items {
			if it.matches(input) {
				got = append(got, it.ID)
			}
		}
		if len(got) != len(want) {
			t.Errorf("search %q matched %v, want %v", input, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("search %q matched %v, want %v", input, got, want)
			}
		}
	}
}
