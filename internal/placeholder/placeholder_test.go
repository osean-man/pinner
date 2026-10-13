package placeholder

import (
	"reflect"
	"testing"
)

func TestFindAndFill(t *testing.T) {
	cmd := "ssh -i <<key:~/.ssh/id_ed25519>> <<user>>@<<host>> -L <<port:5601>>:localhost:<<port>>"
	got := Find(cmd)
	want := []Param{{"key", "~/.ssh/id_ed25519"}, {"user", ""}, {"host", ""}, {"port", "5601"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Find = %+v, want %+v", got, want)
	}
	filled := Fill(cmd, map[string]string{"user": "me", "host": "10.0.0.1", "port": "9200"})
	if filled != "ssh -i ~/.ssh/id_ed25519 me@10.0.0.1 -L 9200:localhost:9200" {
		t.Fatalf("Fill = %q", filled)
	}
}

func TestLaterDefaultFillsEarlierBlank(t *testing.T) {
	got := Find("echo <<x>> <<x:fallback>>")
	if len(got) != 1 || got[0].Default != "fallback" {
		t.Fatalf("got %+v", got)
	}
	if out := Fill("echo <<x>> <<x:fallback>>", nil); out != "echo fallback fallback" {
		t.Fatalf("got %q", out)
	}
}

func TestCommandsWithoutPlaceholdersAreUntouched(t *testing.T) {
	cmds := []string{
		`docker ps --format '{{.Names}}\t{{.Status}}'`,
		`kubectl get pods -o go-template='{{range .items}}{{.metadata.name}}{{"\n"}}{{end}}'`,
		`helm template . --set image={{ .Values.tag }}`,
		"cat <<EOF > out.txt\nhello\nEOF",
		"cat <<'EOF' >> log\nx\nEOF",
		"sort < in.txt > out.txt 2>&1",
		"echo $((1 << 4)) >> bits",
		"git log --format='<%an>' | head",
		"awk '{print $2}' <(ps aux)",
	}
	for _, c := range cmds {
		if p := Find(c); len(p) != 0 {
			t.Errorf("Find(%q) found %+v", c, p)
		}
		if out := Fill(c, map[string]string{"EOF": "boom", "an": "boom"}); out != c {
			t.Errorf("Fill changed %q to %q", c, out)
		}
	}
}
