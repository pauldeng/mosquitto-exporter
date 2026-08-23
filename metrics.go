package main

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	ignoreKeyMetrics = map[string]string{
		"$SYS/broker/timestamp":        "The timestamp at which this particular build of the broker was made. Static.",
		"$SYS/broker/version":          "The version of the broker. Static.",
		"$SYS/broker/clients/active":   "deprecated in favour of $SYS/broker/clients/connected",
		"$SYS/broker/clients/inactive": "deprecated in favour of $SYS/broker/clients/disconnected",
	}
	counterKeyMetrics = map[string]string{
		"$SYS/broker/bytes/received":            "The total number of bytes received since the broker started.",
		"$SYS/broker/bytes/sent":                "The total number of bytes sent since the broker started.",
		"$SYS/broker/messages/received":         "The total number of messages of any type received since the broker started.",
		"$SYS/broker/messages/sent":             "The total number of messages of any type sent since the broker started.",
		"$SYS/broker/publish/bytes/received":    "The total number of PUBLISH bytes received since the broker started.",
		"$SYS/broker/publish/bytes/sent":        "The total number of PUBLISH bytes sent since the broker started.",
		"$SYS/broker/publish/messages/received": "The total number of PUBLISH messages received since the broker started.",
		"$SYS/broker/publish/messages/sent":     "The total number of PUBLISH messages sent since the broker started.",
		"$SYS/broker/publish/messages/dropped":  "The total number of PUBLISH messages that have been dropped due to inflight/queuing limits.",
		"$SYS/broker/uptime":                    "The total number of seconds since the broker started.",
		"$SYS/broker/clients/maximum":           "The maximum number of clients connected simultaneously since the broker started",
		"$SYS/broker/clients/total":             "The total number of clients connected since the broker started.",
	}
	counterMetrics = map[string]*MosquittoCounter{}
	gaugeMetrics   = map[string]prometheus.Gauge{}
)

func resetMetrics() {
	for topic := range counterMetrics {
		if counterMetrics[topic] != nil {
			counterMetrics[topic].Set(0)
		}
	}
	for topic := range gaugeMetrics {
		if gaugeMetrics[topic] != nil {
			gaugeMetrics[topic].Set(0)
		}
	}
}

func processUpdate(topic, payload string) {
	//log.Printf("Got broker update with topic %s and data %s", topic, payload)
	if _, ok := ignoreKeyMetrics[topic]; !ok {
		if _, ok := counterKeyMetrics[topic]; ok {
			// log.Printf("Processing counter metric %s with data %s", topic, payload)
			processCounterMetric(topic, payload)
		} else {
			//log.Printf("Processing gauge metric %s with data %s", topic, payload)
			processGaugeMetric(topic, payload)
		}
	}
}

func processCounterMetric(topic, payload string) {
	if counterMetrics[topic] != nil {
		value := parseValue(payload)
		counterMetrics[topic].Set(value)
	} else {
		// create a mosquitto counter pointer
		mCounter := NewMosquittoCounter(prometheus.NewDesc(
			parseTopic(topic),
			topic,
			[]string{},
			prometheus.Labels{},
		))

		// save it
		counterMetrics[topic] = mCounter
		// register the metric
		prometheus.MustRegister(mCounter)
		// add the first value
		value := parseValue(payload)
		counterMetrics[topic].Set(value)
	}
}

func processGaugeMetric(topic, payload string) {
	if gaugeMetrics[topic] != nil {
		value := parseValue(payload)
		gaugeMetrics[topic].Set(value)
	} else {
		gaugeMetrics[topic] = prometheus.NewGauge(prometheus.GaugeOpts{
			Name: parseTopic(topic),
			Help: topic,
		})
		// register the metric
		prometheus.MustRegister(gaugeMetrics[topic])
		// add the first value
		value := parseValue(payload)
		gaugeMetrics[topic].Set(value)
	}
}

func parseTopic(topic string) string {
	name := strings.Replace(topic, "$SYS/", "", 1)
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, " ", "_")
	name = strings.ReplaceAll(name, "-", "_")
	name = strings.ReplaceAll(name, ".", "_")
	return name
}

func parseValue(payload string) float64 {
	// fmt.Printf("Payload %s \n", payload)
	var validValue = regexp.MustCompile(`-?\d{1,}[.]\d{1,}|\d{1,}`)
	// get the first value of the string
	strArray := validValue.FindAllString(payload, 1)
	if len(strArray) > 0 {
		// parse to float
		value, err := strconv.ParseFloat(strArray[0], 64)
		if err == nil {
			return value
		}
	}
	return 0
}
