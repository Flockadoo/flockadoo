package winglink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Flockadoo/flockadoo/internal/download"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// WithWait shows progress around a slow step. Setup replaces it with the spinner.
var WithWait = func(msg string, fn func() error) error {
	_, _ = fmt.Fprintf(os.Stdout, "%s...\n", msg) //nolint:forbidigo // bring-up runs inside setup, on the operator's terminal (QUAL-8, item K1)
	return fn()
}

// BringUp installs the pinned binaries, starts winglinkd, and joins the tailnet.
// authKey is empty when this machine already joined.
func BringUp(ctx context.Context, loginServer, authKey string) (ip, nodeID string, err error) {
	if goos := runtime.GOOS; goos != "linux" { //nolint:forbidigo // platform branch in shared code (PLAT-2, item H1)
		return "", "", fmt.Errorf("winglink needs a Linux TUN; this machine is %s", goos)
	}
	// Exchange already spent the join code. The key has to be on disk before
	// download or sudo can fail and the next run passes "".
	// A leftover from that failed up is still a pending join: resumeWorker
	// and refreshFlock both call BringUp with "". Treating it as "already
	// joined" reuses the old tailnet and clearAuthKey deletes the unused key.
	supplied := strings.TrimSpace(authKey)
	if authKey, err = persistAuthKey(authKey); err != nil {
		return "", "", err
	}
	freshKey := pendingAuthKey(supplied, authKey)
	// sudo -v prints on the tty. The download spinner clears that line, so
	// the password prompt has to finish before WithWait starts.
	needed, err := installNeeded()
	if err != nil {
		return "", "", err
	}
	if needed {
		if err := ensureSudo(ctx); err != nil {
			return "", "", err
		}
	}
	var cli string
	if err := WithWait("Downloading winglink", func() error {
		var derr error
		cli, derr = ensureBins(ctx)
		return derr
	}); err != nil {
		return "", "", err
	}
	if err := daemonReady(ctx, cli); err != nil {
		if err := ensureSudo(ctx); err != nil {
			return "", "", err
		}
		if err := WithWait("Starting winglink", func() error {
			return ensureDaemon(ctx, cli)
		}); err != nil {
			return "", "", err
		}
	}
	// A new key is a new cluster. Drop the old node so up does not resume
	// it or refuse with "can't change --login-server". logout uses sudo -n,
	// so cache sudo first — otherwise logout is a no-op on an already-running
	// daemon and the online shortcut below would delete the unused key.
	if freshKey {
		if err := ensureSudo(ctx); err != nil {
			return "", "", err
		}
		logout, lerr := commandAsRoot(ctx, cli, "logout")
		if lerr != nil {
			return "", "", lerr
		}
		_ = logout.Run()
	}
	// A saved address is not a connection. Offline still has the old IPv4.
	if st, err := readState(ctx, cli); err == nil && reuseOnlineNode(freshKey, st) {
		clearAuthKey()
		_, _ = fmt.Fprintf(os.Stdout, "Winglink address: %s\n", st.IP) //nolint:forbidigo // bring-up runs inside setup, on the operator's terminal (QUAL-8, item K1)
		return st.IP, st.ID, nil
	}
	if loginServer == "" && authKey == "" {
		return "", "", fmt.Errorf("winglink is not connected and the one-shot key was already used")
	}
	if err := ensureSudo(ctx); err != nil {
		return "", "", err
	}
	keyFile := ""
	if authKey != "" {
		keyFile, err = authKeyPath()
		if err != nil {
			return "", "", err
		}
	}
	cmd, err := commandAsRoot(ctx, cli, upArgs(loginServer, keyFile)...)
	if err != nil {
		return "", "", err
	}
	// up's exit status does not say whether Headscale spent the key.
	// The rejection text lands in the daemon log and, when the CLI prints it, on stderr.
	logAt := fileSize(daemonLogPath())
	var stderr bytes.Buffer
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderr) //nolint:forbidigo // bring-up runs inside setup, on the operator's terminal (QUAL-8, item K1)
	if err := WithWait("Joining the tailnet", func() error {
		if err := cmd.Run(); err != nil {
			if authKeyRejected(stderr.String(), readFrom(daemonLogPath(), logAt)) {
				clearAuthKey()
				return fmt.Errorf("winglink up: one-shot key was rejected: %w", err)
			}
			return fmt.Errorf("winglink up: %w", err)
		}
		clearAuthKey()
		return nil
	}); err != nil {
		return "", "", err
	}
	ip, nodeID, err = waitOnline(ctx, cli)
	if err == nil {
		_, _ = fmt.Fprintf(os.Stdout, "Winglink address: %s\n", ip) //nolint:forbidigo // bring-up runs inside setup, on the operator's terminal (QUAL-8, item K1)
	}
	return ip, nodeID, err
}

// pendingAuthKey is true when this process still has a one-shot key to spend.
// That includes a leftover from a failed `up` when the caller passed "".
func pendingAuthKey(supplied, persisted string) bool {
	return strings.TrimSpace(supplied) != "" || strings.TrimSpace(persisted) != ""
}

// reuseOnlineNode is the already-joined shortcut. A pending key (caller-supplied
// or leftover) is a cluster switch: keep the key and run `up` instead of
// returning the old IP.
func reuseOnlineNode(freshKey bool, st nodeState) bool {
	return !freshKey && st.Online && st.IP != ""
}

// upArgs names the 0600 key file. file: keeps the one-shot key out of argv.
func upArgs(loginServer, authKeyPath string) []string {
	args := []string{"up"}
	if loginServer != "" {
		args = append(args, "--login-server", loginServer)
	}
	if authKeyPath != "" {
		args = append(args, "--auth-key", "file:"+authKeyPath)
	}
	return args
}

const TUN = "winglink0"

// rootArgv is the argv that runs name as root. euid 0 runs it directly.
// Otherwise sudo -n, which uses the credential from ensureSudo.
func rootArgv(euid int, name string, args ...string) (string, []string) {
	if euid == 0 {
		return name, args
	}
	return "sudo", append([]string{"-n", "--", name}, args...)
}

func commandAsRoot(ctx context.Context, name string, args ...string) (*exec.Cmd, error) {
	if err := rootOwned(name); err != nil {
		return nil, fmt.Errorf("refusing to run %s as root: %w", name, err)
	}
	bin, argv := rootArgv(euid(), name, args...)
	cmd := exec.CommandContext(ctx, bin, argv...) //nolint:gosec // G204: name passed rootOwned, or is a fixed command from this package; bin is name or sudo
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout //nolint:forbidigo // sudo and the root command talk to the operator's terminal during setup (QUAL-8, item K1)
	cmd.Stderr = os.Stderr //nolint:forbidigo // sudo and the root command talk to the operator's terminal during setup (QUAL-8, item K1)
	return cmd, nil
}

func startDaemon(ctx context.Context, daemon string) error {
	if err := rootOwned(daemon); err != nil {
		return fmt.Errorf("refusing to run %s as root: %w", daemon, err)
	}
	logf := openDaemonLog()
	defer func() { _ = logf.Close() }()
	if euid() == 0 {
		cmd := exec.Command(daemon) //nolint:forbidigo,noctx // the daemon outlives this process; a context would kill it when setup returns
		cmd.Stdout = logf
		cmd.Stderr = logf
		detach(cmd)
		return cmd.Start()
	}
	// -b asks for nothing (sudo -n) and backgrounds winglinkd after auth.
	cmd := exec.CommandContext(ctx, "sudo", "-n", "-b", "--", daemon) //nolint:gosec // G204: daemon passed rootOwned above
	cmd.Stdin = os.Stdin
	cmd.Stdout = logf
	cmd.Stderr = logf
	return cmd.Run()
}

func daemonLogPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "state", "flockadoo", "log", "winglinkd.log")
}

func openDaemonLog() io.WriteCloser {
	path := daemonLogPath()
	if path == "" || os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return nopWriteCloser{io.Discard}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nopWriteCloser{io.Discard}
	}
	return f
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func authKeyPath() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "auth-key"), nil
}

// persistAuthKey stores a new one-shot key. An empty key is the saved one.
func persistAuthKey(authKey string) (string, error) {
	authKey = strings.TrimSpace(authKey)
	if authKey == "" {
		return loadAuthKey(), nil
	}
	path, err := authKeyPath()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(authKey), 0o600); err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", err
	}
	return authKey, nil
}

func loadAuthKey() string {
	path, err := authKeyPath()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func clearAuthKey() {
	path, err := authKeyPath()
	if err != nil {
		return
	}
	_ = os.Remove(path)
}

// authKeyRejected is true when Headscale refused this key. Other up failures
// may be before the key was spent, so the file has to stay.
func authKeyRejected(stderr, daemonLog string) bool {
	text := stderr + "\n" + daemonLog
	for _, s := range []string{
		"authkey already used",
		"authkey expired",
		"authkey revoked",
		"invalid authkey",
		"invalid pre auth key",
	} {
		if strings.Contains(text, s) {
			return true
		}
	}
	return false
}

func fileSize(path string) int64 {
	if path == "" {
		return 0
	}
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

func readFrom(path string, off int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil || st.Size() <= off {
		return ""
	}
	start := off
	const max = 32 << 10
	if st.Size()-start > max {
		start = st.Size() - max
	}
	b := make([]byte, st.Size()-start)
	n, err := f.ReadAt(b, start)
	if n == 0 || (err != nil && !errors.Is(err, io.EOF)) {
		return ""
	}
	return string(b[:n])
}

const libexecDir = "/usr/local/libexec/flockadoo"

func binPaths() (cli, daemon string) {
	return filepath.Join(libexecDir, "winglink"), filepath.Join(libexecDir, "winglinkd")
}

// stateDir holds the one-shot key. The binaries do not live here.
func stateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".local", "share", "flockadoo", "winglink")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// installNeeded is true when the root-owned binaries are not both present,
// or do not match the pinned release.
func installNeeded() (bool, error) {
	cliName, daemonName, err := hostAsset()
	if err != nil {
		return false, err
	}
	pin, err := embeddedPin()
	if err != nil {
		return false, err
	}
	cli, daemon := binPaths()
	return needsInstall(pin, cliName, daemonName, cli, daemon), nil
}

// needsInstall is true unless both binaries are root-owned and are the
// pinned files: a binary from an older pin, or one changed on disk, is
// replaced through the verified path, never run (plan item S1).
func needsInstall(pin Pin, cliName, daemonName, cli, daemon string) bool {
	if rootOwned(cli) != nil || !matchesPin(pin, cliName, cli) {
		return true
	}
	return daemonName != "" && (rootOwned(daemon) != nil || !matchesPin(pin, daemonName, daemon))
}

func matchesPin(pin Pin, name, path string) bool {
	sum, err := pin.sum(name)
	return err == nil && download.Matches(path, sum)
}

// downloadClient fetches release assets; the binaries are tens of MB.
var downloadClient = &http.Client{Timeout: 10 * time.Minute}

func ensureBins(ctx context.Context) (string, error) {
	cliName, daemonName, err := hostAsset()
	if err != nil {
		return "", err
	}
	// The pinned hashes come first: with nothing pinned for this platform,
	// nothing is downloaded (plan item S1).
	pin, err := embeddedPin()
	if err != nil {
		return "", err
	}
	cliSum, err := pin.sum(cliName)
	if err != nil {
		return "", err
	}
	daemonSum := ""
	if daemonName != "" {
		if daemonSum, err = pin.sum(daemonName); err != nil {
			return "", err
		}
	}
	cli, daemon := binPaths()
	if !needsInstall(pin, cliName, daemonName, cli, daemon) {
		return cli, nil
	}
	// BringUp prompts before the spinner. This covers a direct call.
	if err := ensureSudo(ctx); err != nil {
		return "", err
	}
	if err := lockDownDir(ctx, libexecDir); err != nil {
		return "", err
	}
	if err := installBinary(ctx, downloadURLFor(pin, cliName), cliSum, cli); err != nil {
		return "", err
	}
	if daemonName != "" {
		if err := installBinary(ctx, downloadURLFor(pin, daemonName), daemonSum, daemon); err != nil {
			return "", err
		}
	}
	if err := rootOwned(cli); err != nil {
		return "", fmt.Errorf("winglink install: %w", err)
	}
	if daemonName != "" {
		if err := rootOwned(daemon); err != nil {
			return "", fmt.Errorf("winglinkd install: %w", err)
		}
	}
	return cli, nil
}

// installBinary downloads url, checks it against sum, copies it as root next
// to dest, checks that root-owned copy against sum again, and only then moves
// it to dest. A file that does not match is never written to dest.
func installBinary(ctx context.Context, url, sum, dest string) error {
	if err := rejectSymlink(dest); err != nil {
		return err
	}
	src, err := download.Verified(ctx, downloadClient, url, sum, "")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(src) }()
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	tmp := dest + ".tmp"
	if err := runRoot(ctx, "rm", "-f", tmp); err != nil {
		return err
	}
	if err := writeRoot(ctx, in, tmp); err != nil {
		_ = runRoot(ctx, "rm", "-f", tmp)
		return fmt.Errorf("install %s: %w", dest, err)
	}
	for _, args := range [][]string{{"chmod", "0755", tmp}, {"chown", "root:root", tmp}} {
		if err := runRoot(ctx, args...); err != nil {
			_ = runRoot(ctx, "rm", "-f", tmp)
			return err
		}
	}
	// The download was checked as it arrived, in a file the provider's user
	// owns, and any process of that user can change it after the check. This
	// copy is root's, in a directory the user cannot write (lockDownDir), so
	// checking it here checks the bytes that will run as root.
	if !download.Matches(tmp, sum) {
		_ = runRoot(ctx, "rm", "-f", tmp)
		return fmt.Errorf("install %s: %w", dest, download.ErrMismatch)
	}
	return runRoot(ctx, "mv", "-f", tmp, dest)
}

// runRoot runs a command as root, and writeRoot writes in to a new file at
// path as root: directly when this process is root, through sudo -n
// otherwise. installBinary does everything that needs root through these
// two, so tests, which cannot use sudo, replace them.
var (
	runRoot   = runSudo
	writeRoot = writeAsRoot
)

func writeAsRoot(ctx context.Context, in io.Reader, path string) error {
	if euid() == 0 {
		out, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			_ = out.Close()
			return err
		}
		return out.Close()
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "sudo", "-n", "--", "tee", path) //nolint:gosec // G204: sudo and tee are fixed; path is <dest>.tmp in the root-owned install directory this package builds
	cmd.Stdin = in
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sudo tee %s: %w: %s", path, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func runSudo(ctx context.Context, args ...string) error {
	bin, argv := rootArgv(euid(), args[0], args[1:]...)
	cmd := exec.CommandContext(ctx, bin, argv...) //nolint:gosec // G204: name passed rootOwned, or is a fixed command from this package; bin is name or sudo
	cmd.Stdout = os.Stdout                        //nolint:forbidigo // sudo and the root command talk to the operator's terminal during setup (QUAL-8, item K1)
	cmd.Stderr = os.Stderr                        //nolint:forbidigo // sudo and the root command talk to the operator's terminal during setup (QUAL-8, item K1)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sudo %s: %w", args[0], err)
	}
	return nil
}

func ensureDaemon(ctx context.Context, cli string) error {
	if daemonReady(ctx, cli) == nil {
		return nil
	}
	daemon := filepath.Join(filepath.Dir(cli), "winglinkd")
	if err := startDaemon(ctx, daemon); err != nil {
		return fmt.Errorf("start winglinkd: %w", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		if err := daemonReady(ctx, cli); err == nil {
			return nil
		} else {
			last = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	msg := "winglinkd did not answer"
	if tail := tailFile(daemonLogPath(), 800); tail != "" {
		msg += ": " + tail
	} else if last != nil {
		msg += ": " + last.Error()
	}
	return fmt.Errorf("%s", msg)
}

// daemonReady is true once status returns JSON. An address shows up only after up.
func daemonReady(ctx context.Context, cli string) error {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, cli, "status", "--json").Output()
	if statusAnswered(out) {
		return nil
	}
	if err == nil {
		return fmt.Errorf("empty status")
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(exit.Stderr) > 0 {
		return fmt.Errorf("%s", strings.TrimSpace(string(exit.Stderr)))
	}
	return err
}

func statusAnswered(out []byte) bool {
	out = bytes.TrimSpace(out)
	return len(out) > 0 && json.Valid(out)
}

func tailFile(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil || len(bytes.TrimSpace(b)) == 0 {
		return ""
	}
	b = bytes.TrimSpace(b)
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return string(b)
}

type nodeState struct {
	IP     string
	ID     string
	Online bool
}

func readState(ctx context.Context, cli string) (nodeState, error) {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, cli, "status", "--json").Output()
	if err != nil {
		ip, ipErr := ipv4(ctx, cli)
		return nodeState{IP: ip}, ipErr
	}
	st, err := parseStatus(out)
	if err != nil {
		ip, ipErr := ipv4(ctx, cli)
		return nodeState{IP: ip}, ipErr
	}
	return st, nil
}

func parseStatus(out []byte) (nodeState, error) {
	var st struct {
		BackendState string `json:"BackendState"`
		Self         struct {
			ID           string   `json:"ID"`
			Online       bool     `json:"Online"`
			TailscaleIPs []string `json:"TailscaleIPs"`
		} `json:"Self"`
	}
	if err := json.Unmarshal(out, &st); err != nil {
		return nodeState{}, err
	}
	// Self.Online stays true on an expired node that sits in NeedsLogin.
	parsed := nodeState{ID: st.Self.ID, Online: st.Self.Online && st.BackendState == "Running"}
	for _, addr := range st.Self.TailscaleIPs {
		if strings.Contains(addr, ".") {
			parsed.IP = addr
			break
		}
	}
	return parsed, nil
}

// waitOnline polls until the daemon is connected to headscale.
// up can return while Self.Online is still false.
func waitOnline(ctx context.Context, cli string) (string, string, error) {
	deadline := time.Now().Add(15 * time.Second)
	var last error
	for {
		st, err := readState(ctx, cli)
		if err == nil && st.Online && st.IP != "" {
			return st.IP, st.ID, nil
		}
		if err != nil {
			last = err
		} else {
			last = fmt.Errorf("winglink is offline")
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			if ctx.Err() != nil {
				return "", "", ctx.Err()
			}
			return "", "", last
		}
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// CLI is the root-owned winglink binary. A user-writable PATH entry is ignored.
func CLI() (string, error) {
	cli, err := trustedCLI()
	if err != nil {
		return "", fmt.Errorf("winglink is not installed: %w", err)
	}
	return cli, nil
}

func trustedCLI() (string, error) {
	cli, _ := binPaths()
	if err := rootOwned(cli); err == nil {
		return cli, nil
	}
	path, err := exec.LookPath("winglink")
	if err != nil {
		return "", err
	}
	if err := rootOwned(path); err != nil {
		return "", err
	}
	return path, nil
}

func ipv4(ctx context.Context, cli string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, cli, "ip", "-4").Output()
	if err != nil {
		return "", err
	}
	ip := strings.TrimSpace(string(out))
	if ip == "" {
		return "", fmt.Errorf("winglink has no ipv4")
	}
	return ip, nil
}

// Tailnet is this machine's Winglink IPv4 and how many other nodes are online.
func Status(ctx context.Context) (ip string, onlinePeers int) {
	cli, err := trustedCLI()
	if err != nil {
		return "", 0
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, cli, "status", "--json").Output() //nolint:gosec // G204: cli is trustedCLI's root-owned binary
	if err != nil {
		ip, _ = ipv4(ctx, cli)
		return ip, 0
	}
	var st struct {
		Self struct {
			TailscaleIPs []string `json:"TailscaleIPs"`
		} `json:"Self"`
		Peer map[string]struct {
			Online bool `json:"Online"`
		} `json:"Peer"`
	}
	if json.Unmarshal(out, &st) != nil {
		ip, _ = ipv4(ctx, cli)
		return ip, 0
	}
	for _, addr := range st.Self.TailscaleIPs {
		if strings.Contains(addr, ".") {
			ip = addr
			break
		}
	}
	for _, p := range st.Peer {
		if p.Online {
			onlinePeers++
		}
	}
	return ip, onlinePeers
}
