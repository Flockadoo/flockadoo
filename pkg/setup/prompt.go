package setup

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

func promptLine(in io.Reader, out io.Writer, question, def string) (string, error) {
	if def != "" {
		_, _ = fmt.Fprintf(out, "%s [%s]: ", question, def)
	} else {
		_, _ = fmt.Fprintf(out, "%s: ", question)
	}
	sc := bufio.NewScanner(in)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return "", err
		}
		return def, io.EOF
	}
	s := strings.TrimSpace(sc.Text())
	if s == "" {
		return def, nil
	}
	return s, nil
}

// promptYes asks a yes/no question that defaults to yes.
func promptYes(in io.Reader, out io.Writer, question string) (bool, error) {
	s, err := promptLine(in, out, question+" (Y/n)", "")
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	switch strings.ToLower(s) {
	case "n", "no":
		return false, nil
	default:
		return true, nil
	}
}

func promptChoice(in io.Reader, out io.Writer, n, def int) (int, error) {
	s, err := promptLine(in, out, fmt.Sprintf("Choose 1-%d", n), strconv.Itoa(def))
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, err
	}
	i, convErr := strconv.Atoi(strings.TrimSpace(s))
	if convErr != nil || i < 1 || i > n {
		return 0, fmt.Errorf("invalid choice %q", s)
	}
	return i, nil
}

func isTTY(f *os.File) bool {
	if f == nil {
		return false
	}
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

// IsInteractive reports whether stdin is a terminal.
func IsInteractive() bool {
	return isTTY(os.Stdin)
}
