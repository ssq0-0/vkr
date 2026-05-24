package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type TokenManager struct {
	secret []byte
	ttl    time.Duration
}

func NewTokenManager(secret string, ttl time.Duration) *TokenManager {
	if secret == "" {
		secret = "diplom-local-secret"
	}
	return &TokenManager{secret: []byte(secret), ttl: ttl}
}

func (m *TokenManager) Issue(user User) (string, time.Time, error) {
	expiresAt := time.Now().Add(m.ttl).UTC()
	payload := fmt.Sprintf("%d:%s:%d", user.ID, user.Username, expiresAt.Unix())
	signature := m.sign(payload)
	token := base64.RawURLEncoding.EncodeToString([]byte(payload + ":" + signature))
	return token, expiresAt, nil
}

func (m *TokenManager) Verify(token string) (User, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return User{}, errors.New("invalid token encoding")
	}
	parts := strings.Split(string(raw), ":")
	if len(parts) != 4 {
		return User{}, errors.New("invalid token")
	}
	payload := strings.Join(parts[:3], ":")
	if !hmac.Equal([]byte(parts[3]), []byte(m.sign(payload))) {
		return User{}, errors.New("invalid token signature")
	}
	expiresUnix, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return User{}, errors.New("invalid token expiration")
	}
	if time.Now().Unix() > expiresUnix {
		return User{}, errors.New("token expired")
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return User{}, errors.New("invalid user id")
	}
	return User{ID: id, Username: parts[1]}, nil
}

func (m *TokenManager) HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	sum := passwordDigest(salt, password)
	return hex.EncodeToString(salt) + ":" + hex.EncodeToString(sum), nil
}

func CheckPassword(encoded, password string) bool {
	parts := strings.Split(encoded, ":")
	if len(parts) != 2 {
		return false
	}
	salt, err := hex.DecodeString(parts[0])
	if err != nil {
		return false
	}
	expected, err := hex.DecodeString(parts[1])
	if err != nil {
		return false
	}
	actual := passwordDigest(salt, password)
	return hmac.Equal(expected, actual)
}

func passwordDigest(salt []byte, password string) []byte {
	h := sha256.New()
	h.Write(salt)
	h.Write([]byte(password))
	sum := h.Sum(nil)
	for i := 0; i < 10000; i++ {
		h.Reset()
		h.Write(sum)
		h.Write(salt)
		sum = h.Sum(nil)
	}
	return sum
}

func (m *TokenManager) sign(payload string) string {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}
