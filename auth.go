package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Identity is the HOME user, which is the OpenPak core account and nothing else.
type Identity struct {
	AccountID string
	Nickname  string
	PID       uint64
	Via       string // "id_token" or "proxy"
}

type AuthError struct {
	Status int
	Msg    string
}

func (e *AuthError) Error() string { return e.Msg }

// Authenticator resolves a request to an account. Two ways in, no HOME login of our own:
//   - a Switch Nintendo Account id token (Authorization: Bearer ...), proved by nx-baas;
//   - the website/app proxy: X-Internal-Key plus X-OpenPak-Account (never exposed publicly).
type Authenticator struct {
	InternalKey  string
	NXURL, NXKey string
	AppIDs       map[string]bool // allowed nintendo.ai values; empty = any proven token (TODO(capture))
	Client       *http.Client
	mu           sync.Mutex
	cache        map[[32]byte]cached
}

type cached struct {
	id  *Identity
	exp time.Time
}

func keyEq(a, b string) bool {
	return a != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (au *Authenticator) Authenticate(ctx context.Context, r *http.Request) (*Identity, error) {
	if k := r.Header.Get("X-Internal-Key"); k != "" {
		if !keyEq(k, au.InternalKey) {
			return nil, &AuthError{401, "bad internal key"}
		}
		acct := strings.TrimSpace(r.Header.Get("X-OpenPak-Account"))
		if acct == "" || len(acct) > 128 {
			return nil, &AuthError{400, "X-OpenPak-Account required"}
		}
		return &Identity{AccountID: acct, Via: "proxy"}, nil
	}
	h := r.Header.Get("Authorization")
	tok, ok := strings.CutPrefix(h, "Bearer ")
	if !ok || tok == "" {
		return nil, &AuthError{401, "missing credentials"}
	}
	return au.idToken(ctx, tok)
}

func (au *Authenticator) idToken(ctx context.Context, tok string) (*Identity, error) {
	sum := sha256.Sum256([]byte(tok))
	au.mu.Lock()
	if c, ok := au.cache[sum]; ok && time.Now().Before(c.exp) {
		au.mu.Unlock()
		return c.id, nil
	}
	au.mu.Unlock()

	body, _ := json.Marshal(map[string]string{"id_token": tok})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, au.NXURL+"/internal/switch/identity", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Key", au.NXKey)
	cl := au.Client
	if cl == nil {
		cl = &http.Client{Timeout: 8 * time.Second}
	}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, &AuthError{503, "identity service unavailable"}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	switch resp.StatusCode {
	case 200:
	case 401: // not proven: the console fetches a fresh token and retries
		return nil, &AuthError{401, "id_token not proven"}
	case 403:
		return nil, &AuthError{403, "account locked"}
	case 404:
		return nil, &AuthError{403, "no OpenPak account is linked to this console"}
	default:
		return nil, &AuthError{503, "identity service error"}
	}
	var out struct {
		PID           uint64 `json:"pid"`
		Nickname      string `json:"nickname"`
		AccountID     string `json:"account_id"`
		ApplicationID string `json:"application_id"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, &AuthError{503, "identity service error"}
	}
	if out.AccountID == "" {
		return nil, &AuthError{403, "link this console to an OpenPak account first"}
	}
	if len(au.AppIDs) > 0 && !au.AppIDs[strings.ToLower(out.ApplicationID)] {
		return nil, &AuthError{403, "token is not for this application"}
	}
	id := &Identity{AccountID: out.AccountID, Nickname: out.Nickname, PID: out.PID, Via: "id_token"}
	au.mu.Lock()
	if au.cache == nil || len(au.cache) > 4096 {
		au.cache = map[[32]byte]cached{}
	}
	au.cache[sum] = cached{id, time.Now().Add(60 * time.Second)}
	au.mu.Unlock()
	return id, nil
}
