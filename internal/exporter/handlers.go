package exporter

import (
	"fmt"
	"log/slog"
	"net/http"
)

/*
 * Root and Healthcheck
 */

func serveVersion(version string) http.HandlerFunc {
	landingPage := fmt.Appendf(nil, `<html>
<head><title>Mosquitto exporter</title></head>
<body>
<h1>Mosquitto exporter</h1>
<p>%s</p>
<p><a href='/metrics'>Metrics</a></p>
</body>
</html>
`, version)

	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, err := w.Write(landingPage); err != nil {
			slog.Warn("writing landing page response", "error", err)
		}
	}
}
