package auth

import (
	"crypto/hmac"
	"crypto/sha256"
)

func hmacSHA256(data, key []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

func hmacEqual(a, b []byte) bool { return hmac.Equal(a, b) }
