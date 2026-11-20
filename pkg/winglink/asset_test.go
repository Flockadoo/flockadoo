package winglink

import "testing"

func TestAssetNames(t *testing.T) {
	cli, daemon, err := Asset("linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if cli != "winglink-linux-amd64" || daemon != "winglinkd-linux-amd64" {
		t.Fatalf("linux amd64 = %s %s", cli, daemon)
	}
	cli, daemon, err = Asset("linux", "arm64")
	if err != nil || cli != "winglink-linux-arm64" || daemon != "winglinkd-linux-arm64" {
		t.Fatalf("arm64 = %s %s %v", cli, daemon, err)
	}
	cli, daemon, err = Asset("windows", "amd64")
	if err != nil || cli != "winglink-windows-amd64.zip" || daemon != "" {
		t.Fatalf("windows = %s %q %v", cli, daemon, err)
	}
	if _, _, err := Asset("linux", "386"); err == nil {
		t.Fatal("386 was accepted")
	}
}
