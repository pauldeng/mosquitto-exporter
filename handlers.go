package main

import (
	"fmt"
	"net/http"

	log "github.com/sirupsen/logrus"
)

/*
 * Root and Healthcheck
 */

var landingPage = []byte(fmt.Sprintf(`<html>
<head><title>Mosquitto exporter</title></head>
<body>
<h1>Mosquitto exporter</h1>
<p>%s</p>
<p><a href='/metrics'>Metrics</a></p>
</body>
</html>
`, versionString()))

func serveVersion(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := w.Write(landingPage); err != nil {
		log.Warnf("writing landing page response: %s", err)
	}
}
