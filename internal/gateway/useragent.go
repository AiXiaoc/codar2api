package gateway

const defaultCodarUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) codar-desktop/0.3.2 Chrome/138.0.7204.251 Electron/37.10.3 Safari/537.36"

func modelUserAgent(settings M, fallback string) string {
	if yes(settings["use_codar_ua"]) {
		return str(settings["codar_ua"], defaultCodarUA)
	}
	return fallback
}
