package gateway

const defaultCodarUA = "codar-desktop/0.3.3"

func modelUserAgent(settings M, incoming ...string) string {
	if enabled, ok := settings["use_codar_ua"].(bool); ok && !enabled {
		if len(incoming) > 0 {
			return incoming[0]
		}
		return "Go-http-client/1.1"
	}
	return str(settings["codar_ua"], defaultCodarUA)
}
