package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	publicChallengeTTL = 5 * time.Minute
	publicMaxInputSize = 2 << 20
	publicMaxConvert   = 500
)

var errInvalidChallenge = errors.New("验证已过期或无效，请重新验证")

type publicChallenge struct {
	ExpiresAt int64  `json:"expires_at"`
	Nonce     string `json:"nonce"`
}

type publicChallengeEntry struct {
	Answer    int
	ExpiresAt time.Time
}

type publicPayloadEntry struct {
	Payload   string
	ExpiresAt time.Time
}

type publicConversion struct {
	Format      string   `json:"format"`
	FormatLabel string   `json:"format_label"`
	Count       int      `json:"count"`
	Nodes       []string `json:"nodes"`
	Base64      string   `json:"base64"`
	Text        string   `json:"text"`
}

func (a *App) handlePublicChallenge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	token, question, expiresAt, err := a.newPublicChallenge()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "暂时无法生成验证问题"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      token,
		"question":   question,
		"expires_at": expiresAt,
	})
}

func (a *App) handlePublicConvert(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var request struct {
		Token  string `json:"token"`
		Answer string `json:"answer"`
		Input  string `json:"input"`
		Target string `json:"target"`
	}
	if err := decodeJSON(r, &request, publicMaxInputSize); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if err := a.verifyPublicChallenge(request.Token, request.Answer); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	result := parseNodeInput(request.Input)
	if len(result.URIs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": nodeImportError(result.Format).Error()})
		return
	}
	nodes := uniqueStrings(result.URIs)
	if len(nodes) > publicMaxConvert {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("单次最多转换 %d 个节点", publicMaxConvert)})
		return
	}

	target := normalizePublicTarget(request.Target)
	text := strings.Join(nodes, "\n")
	if target != "" && target != "base64" {
		converted, err := a.convertPublicNodes(r, nodes, target)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		text = converted
	}
	writeJSON(w, http.StatusOK, publicConversion{
		Format:      result.Format,
		FormatLabel: formatNodeImportLabel(result.Format),
		Count:       len(nodes),
		Nodes:       nodes,
		Base64:      encodeBase64(text),
		Text:        text,
	})
}

func (a *App) newPublicChallenge() (string, string, int64, error) {
	left := randomInt(12, 89)
	right := randomInt(2, 49)
	operator := randomInt(0, 1)
	answer := left + right
	question := fmt.Sprintf("%d + %d = ?", left, right)
	if operator == 0 {
		if left < right {
			left, right = right, left
		}
		answer = left - right
		question = fmt.Sprintf("%d - %d = ?", left, right)
	}
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return "", "", 0, err
	}
	challenge := publicChallenge{
		ExpiresAt: time.Now().Add(publicChallengeTTL).Unix(),
		Nonce:     hex.EncodeToString(nonceBytes),
	}
	token, err := a.signPublicChallenge(challenge)
	if err == nil {
		a.publicStateMu.Lock()
		a.cleanupPublicStateLocked(time.Now())
		a.publicChallenges[token] = publicChallengeEntry{Answer: answer, ExpiresAt: time.Unix(challenge.ExpiresAt, 0)}
		a.publicStateMu.Unlock()
	}
	return token, question, challenge.ExpiresAt, err
}

func (a *App) signPublicChallenge(challenge publicChallenge) (string, error) {
	payload, err := json.Marshal(challenge)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, a.publicChallengeKey())
	_, _ = mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (a *App) verifyPublicChallenge(token, answer string) error {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 2 {
		return errInvalidChallenge
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return errInvalidChallenge
	}
	mac := hmac.New(sha256.New, a.publicChallengeKey())
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return errInvalidChallenge
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return errInvalidChallenge
	}
	var challenge publicChallenge
	if err := json.Unmarshal(payload, &challenge); err != nil {
		return errInvalidChallenge
	}
	if challenge.ExpiresAt < time.Now().Unix() || challenge.Nonce == "" {
		return errInvalidChallenge
	}
	now := time.Now()
	a.publicStateMu.Lock()
	a.cleanupPublicStateLocked(now)
	entry, ok := a.publicChallenges[strings.TrimSpace(token)]
	if ok {
		delete(a.publicChallenges, strings.TrimSpace(token))
	}
	a.publicStateMu.Unlock()
	if !ok || now.After(entry.ExpiresAt) {
		return errInvalidChallenge
	}
	parsedAnswer, err := strconv.Atoi(strings.TrimSpace(answer))
	if err != nil || parsedAnswer != entry.Answer {
		return errors.New("验证答案不正确，请重试")
	}
	return nil
}

func (a *App) cleanupPublicStateLocked(now time.Time) {
	for token, entry := range a.publicChallenges {
		if now.After(entry.ExpiresAt) {
			delete(a.publicChallenges, token)
		}
	}
	for id, entry := range a.publicPayloads {
		if now.After(entry.ExpiresAt) {
			delete(a.publicPayloads, id)
		}
	}
}

func (a *App) storePublicPayload(payload string) (string, error) {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return "", errors.New("转换内容为空")
	}
	nonce := make([]byte, 18)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	id := base64.RawURLEncoding.EncodeToString(nonce)
	now := time.Now()
	a.publicStateMu.Lock()
	a.cleanupPublicStateLocked(now)
	a.publicPayloads[id] = publicPayloadEntry{Payload: payload, ExpiresAt: now.Add(publicChallengeTTL)}
	a.publicStateMu.Unlock()
	return id, nil
}

func (a *App) getPublicPayload(id string) (string, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", false
	}
	now := time.Now()
	a.publicStateMu.Lock()
	a.cleanupPublicStateLocked(now)
	entry, ok := a.publicPayloads[id]
	a.publicStateMu.Unlock()
	if !ok || now.After(entry.ExpiresAt) {
		return "", false
	}
	return entry.Payload, true
}

func (a *App) internalPublicURL(id string) string {
	base := strings.TrimRight(a.internalBaseURL, "/")
	if base == "" {
		base = "http://127.0.0.1:" + a.config.Port
	}
	return base + "/internal/public/" + id
}

func (a *App) publicChallengeKey() []byte {
	seed := a.config.BootstrapToken + "|" + a.config.DBPath + "|sub-hub-public-captcha"
	sum := sha256.Sum256([]byte(seed))
	return sum[:]
}

func randomInt(minimum, maximum int) int {
	if maximum <= minimum {
		return minimum
	}
	span := maximum - minimum + 1
	value, err := rand.Int(rand.Reader, big.NewInt(int64(span)))
	if err != nil {
		return minimum
	}
	return minimum + int(value.Int64())
}

func normalizePublicTarget(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "base64", "nodes", "uri":
		return ""
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func (a *App) convertPublicNodes(r *http.Request, nodes []string, target string) (string, error) {
	raw := strings.Join(nodes, "\n")
	payloadID, err := a.storePublicPayload(raw)
	if err != nil {
		return "", err
	}
	query := url.Values{}
	query.Set("target", target)
	query.Set("url", a.internalPublicURL(payloadID))
	converterURL := strings.TrimRight(a.config.SubconverterURL, "/") + "/sub?" + query.Encode()

	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, converterURL, nil)
	if err != nil {
		return "", errors.New("转换器不可用")
	}
	response, err := a.client.Do(request)
	if err != nil {
		return "", errors.New("转换器不可用")
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return "", errors.New("读取转换结果失败")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("转换器返回 %d", response.StatusCode)
	}
	if len(strings.TrimSpace(string(payload))) == 0 {
		return "", errors.New("转换器没有返回内容")
	}
	return strings.TrimSpace(string(payload)), nil
}
