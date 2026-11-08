package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

// The user-level half of the language setting.
//
// The platform ships one default (`agent.language.default`, seeded to "zh"); an
// account may override it, and the effective language is what the runtime states
// in the per-turn language note. There is deliberately no "follow the latest
// message" mode: the language is a setting, not a guess.

// supportedLanguages are the tags the console offers. The stored value is always
// a primary subtag, so "zh-CN" and "zh-Hans" both land on "zh".
var supportedLanguages = map[string]bool{
	"zh": true, "en": true, "ja": true, "ko": true, "ru": true,
	"ar": true, "fr": true, "de": true, "es": true, "pt": true,
}

// normalizeLanguage lowercases a tag and keeps its primary subtag. It returns
// "" for an empty value (the account inherits the platform default).
func normalizeLanguage(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if tag == "" {
		return ""
	}
	if i := strings.IndexAny(tag, "-_"); i > 0 {
		tag = tag[:i]
	}
	return tag
}

// platformLanguage returns the configured platform default ("" when unset).
func (s *Server) platformLanguage() string {
	item, err := s.store.GetConfigItem("agent", "agent.language.default")
	if err != nil {
		return ""
	}
	return normalizeLanguage(item.Value)
}

type preferencesResponse struct {
	Language string `json:"language"`
	// Source says which level the value came from: "user" when the account set
	// it, "platform" when it is inherited.
	Source string `json:"source"`
}

// withUserLanguage folds the account's language into the settings the runtime
// receives, unless the agent already carries its own. The platform default is
// deliberately not folded: the runtime reads it from the config items, so the
// two levels stay distinguishable and the admin's default keeps working without
// rewriting every account.
func (s *Server) withUserLanguage(settings map[string]string, userID string) map[string]string {
	if _, ok := settings["language"]; ok {
		return settings
	}
	lang, err := s.store.GetUserPreferredLanguage(userID)
	if err != nil || lang == "" {
		return settings
	}
	out := make(map[string]string, len(settings)+1)
	for k, v := range settings {
		out[k] = v
	}
	out["language"] = lang
	return out
}

// restGetPreferences returns the effective language and where it came from.
func (s *Server) restGetPreferences(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromQuery(r)
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	own, err := s.store.GetUserPreferredLanguage(userID)
	if err != nil {
		slog.Error("get preferred language", "user", userID, "error", err)
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}
	if own != "" {
		writeJSON(w, http.StatusOK, preferencesResponse{Language: own, Source: "user"})
		return
	}
	writeJSON(w, http.StatusOK, preferencesResponse{Language: s.platformLanguage(), Source: "platform"})
}

// restSetPreferences stores the account's language; an empty value clears it so
// the account inherits the platform default again.
func (s *Server) restSetPreferences(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserID   string `json:"user_id"`
		Language string `json:"language"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	userID := body.UserID
	if userID == "" {
		userID = userIDFromQuery(r)
	}
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	lang := normalizeLanguage(body.Language)
	if lang != "" && !supportedLanguages[lang] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported language"})
		return
	}
	if err := s.store.SetUserPreferredLanguage(userID, lang); err != nil {
		slog.Error("set preferred language", "user", userID, "error", err)
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}
	s.recordAudit(r, "user.preferences.language", "user", userID, map[string]any{
		"language": lang,
	})
	// Clearing the choice goes back to the platform default, and the answer says
	// so: the console shows the effective language right after a save.
	if lang == "" {
		writeJSON(w, http.StatusOK, preferencesResponse{Language: s.platformLanguage(), Source: "platform"})
		return
	}
	writeJSON(w, http.StatusOK, preferencesResponse{Language: lang, Source: "user"})
}
