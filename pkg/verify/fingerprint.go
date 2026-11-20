package verify

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// FileHash computes the per-file hash using the manifest hash_method.
// Must match inferoute-node/scripts/bootstrap_approved_model.sh.
func FileHash(path, method string) (string, error) {
	switch method {
	case "safetensors_header":
		return safetensorsHeaderSHA256(path)
	case "full":
		return fullFileSHA256(path)
	default:
		return "", fmt.Errorf("unsupported hash_method: %s", method)
	}
}

func fullFileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// maxSafetensorsHeader is the largest header the safetensors format allows
// (the reference implementation refuses more than 100 MB). Real headers are
// kilobytes to a few megabytes.
const maxSafetensorsHeader = 100_000_000

// safetensorsHeaderSHA256 hashes 8-byte LE header length + header JSON bytes.
func safetensorsHeaderSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	var lenBuf [8]byte
	if _, err := io.ReadFull(f, lenBuf[:]); err != nil {
		return "", fmt.Errorf("safetensors file too short: %w", err)
	}
	headerLen := binary.LittleEndian.Uint64(lenBuf[:])
	if headerLen > maxSafetensorsHeader {
		return "", fmt.Errorf("safetensors header of %d bytes is too large (the format allows %d)", headerLen, maxSafetensorsHeader)
	}
	header := make([]byte, headerLen)
	if _, err := io.ReadFull(f, header); err != nil {
		return "", fmt.Errorf("truncated safetensors header: %w", err)
	}

	payload := append(lenBuf[:], header...)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}
