package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"gitee.com/kobeyoung81/nanoprotocol"
)

// handleValidateToken validates a JWT against nanoauth's userinfo endpoint and
// returns the resolved user identity. It is used by the gateway web channel to
// authenticate browser clients.
func (s *Server) handleValidateToken(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.ValidateTokenRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}
	if req.Token == "" {
		resp := nanoprotocol.NewErrorMessage("token is required")
		return &resp
	}
	if s.nanoauthURL == "" {
		resp := nanoprotocol.NewErrorMessage("auth not configured")
		return &resp
	}

	httpReq, err := http.NewRequest(http.MethodGet, s.nanoauthURL+"/auth/v1/userinfo", nil)
	if err != nil {
		resp := nanoprotocol.NewErrorMessage("auth request failed")
		return &resp
	}
	httpReq.Header.Set("Authorization", "Bearer "+req.Token)
	httpReq.Header.Set("Accept", "application/json")

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(httpReq)
	if err != nil {
		slog.Warn("validate token: userinfo request failed", "error", err)
		respErr := nanoprotocol.NewErrorMessage("auth unavailable")
		return &respErr
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	out := nanoprotocol.ValidateTokenResponse{Valid: false}
	if resp.StatusCode == http.StatusOK {
		var userinfo struct {
			Sub  string `json:"sub"`
			Role string `json:"role"`
		}
		if err := json.Unmarshal(body, &userinfo); err == nil && userinfo.Sub != "" {
			out.Valid = true
			out.UserID = userinfo.Sub
			out.Role = userinfo.Role
		}
	}

	respMsg, _ := nanoprotocol.NewMessage(nanoprotocol.TypeTokenValid, out)
	return &respMsg
}
