package redis

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"strings"

	"github.com/jxskiss/base62"
)

func GenerateToken(prefix string, entropyLength int) (string, error) {
	// generate high entropy random numbers
	randomSeq := make([]byte, entropyLength)
	_, err := rand.Read(randomSeq)
	if err != nil {
		return "", fmt.Errorf("fail to generate auth token: %w", err)
	}

	// eval CRC32 checksum
	checksum := crc32.ChecksumIEEE(randomSeq)

	// transform checksum to 4 bytes slice
	checksumBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(checksumBytes, checksum)

	// concating and encoding
	payload := append(randomSeq, checksumBytes...)
	encodingToken := base62.EncodeToString(payload)

	return fmt.Sprintf("%s-%s", prefix, encodingToken), nil
}

func CheckTokenFormat(prefix string, entropyLength int, token string) bool {
	// check prefix
	expectedPrefix := prefix + "-"
	if !strings.HasPrefix(token, expectedPrefix) {
		return false
	}

	// check base64 format
	encodedPart := strings.TrimPrefix(token, expectedPrefix)
	payload, err := base62.DecodeString(encodedPart)
	if err != nil {
		return false
	}

	// check payload length
	if len(payload) != entropyLength+4 {
		return false
	}

	// check CRC32
	entropy := payload[:entropyLength]
	checksumBytes := payload[entropyLength:]

	expectedChecksum := crc32.ChecksumIEEE(entropy)
	expectedChecksumBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(expectedChecksumBytes, expectedChecksum)

	return subtle.ConstantTimeCompare(checksumBytes, expectedChecksumBytes) == 1
}
