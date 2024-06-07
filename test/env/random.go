package env

import (
	"encoding/hex"
	"fmt"
	"math/rand"
	"time"
)

func RandomNamespace() string {
	return RandomName("testns", 16)
}

// RandomName generates a random name of n length with the provided
// prefix. If prefix is omitted, the then entire name is random char.
func RandomName(prefix string, n int) string {
	if n == 0 {
		n = 32
	}
	if len(prefix) >= n {
		return prefix
	}
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	p := make([]byte, n)
	r.Read(p)
	if prefix == "" {
		return hex.EncodeToString(p)[:n]
	}
	return fmt.Sprintf("%s-%s", prefix, hex.EncodeToString(p))[:n]
}
