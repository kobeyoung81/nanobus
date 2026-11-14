package server

// The account's language, folded into what the runtime is told.
//
// Two levels: the platform ships a default (`agent.language.default`, seeded to
// "zh") and an account may override it. The runtime receives the override as a
// setting when there is one, and reads the default itself — so the two levels stay
// distinguishable and an admin's default keeps working without rewriting every
// account.
//
// The setting is only read here: writing it, and answering "what language does this
// account speak", is nanoauth's (`internal/v1/users/{id}/preferences`).

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
