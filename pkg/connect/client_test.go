package connect

import "testing"

func TestDialURL(t *testing.T) {
	got, err := DialURL("https://core.flockadoo.ai")
	if err != nil {
		t.Fatal(err)
	}
	if got != "wss://core.flockadoo.ai/api/connect/ws" {
		t.Fatalf("url %s", got)
	}
	got, err = DialURL("http://localhost:80")
	if err != nil {
		t.Fatal(err)
	}
	if got != "ws://localhost:80/api/connect/ws" {
		t.Fatalf("url %s", got)
	}
}
