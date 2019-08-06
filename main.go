package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"text/template"

	alertmanagerWebhook "github.com/prometheus/alertmanager/notify/webhook"
	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var DefaultFuncs = template.FuncMap{
	"toUpper": strings.ToUpper,
	"toLower": strings.ToLower,
	"title":   strings.Title,
	// join is equal to strings.Join but inverts the argument order
	// for easier pipelining in templates.
	"join": func(sep string, s []string) string {
		return strings.Join(s, sep)
	},
	"match": regexp.MatchString,
	"reReplaceAll": func(pattern, repl, text string) string {
		re := regexp.MustCompile(pattern)
		return re.ReplaceAllString(text, repl)
	},
}

var (
	alertsErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "zulip_alerts_errors",
		Help: "The number of errors while trying to send alerts",
	}, []string{"type"})
	alertsSent = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "zulip_alerts_sent_total",
		Help: "The total number of processed alerts",
	}, []string{"topic"})
)

var (
	topicTpl   *template.Template = template.New("topic").Funcs(template.FuncMap(DefaultFuncs))
	messageTpl *template.Template = template.New("message").Funcs(template.FuncMap(DefaultFuncs))
)

func initTpl() {
	var err error
	topicTpl, err = topicTpl.Parse(viper.GetString("templates.topic"))
	if err != nil {
		panic(err)
	}

	messageTpl, err = messageTpl.Parse(viper.GetString("templates.message"))

	if err != nil {
		panic(err)
	}

	log.Println("Templates compiled")
}

func formatAlerts(msg *alertmanagerWebhook.Message) (string, string, error) {
	var topic bytes.Buffer
	if err := topicTpl.Execute(&topic, &msg.Data); err != nil {
		return "", "", err
	}

	var message bytes.Buffer
	if err := messageTpl.Execute(&message, &msg.Data); err != nil {
		return "", "", err
	}

	return topic.String(), message.String(), nil
}

func main() {
	flag.Int("port", 3000, "The port on which to serve the app")
	flag.String("url", "https://team.zulipchat.com", "URL to your Zulip instance")
	flag.String("username", "zulip@example.com", "The username to use when connecting to Zulip")
	flag.String("password", "hunter2", "The password to use when connecting to Zulip")
	flag.String("stream", "alerts", "The stream where to post the alerts")
	flag.String("templates.topic", "{{ .GroupLabels.alertname }}", "Go template for the topic")
	flag.String("templates.message", "[{{ .Status | toUpper }}{{ if eq .Status \"firing\" }}:{{ .Alerts.Firing | len }}{{ end }}] {{ .GroupLabels.SortedPairs.Values | join \" \" }} {{ if gt (len .CommonLabels) (len .GroupLabels) }}({{ with .CommonLabels.Remove .GroupLabels.Names }}{{ .Values | join \" \" }}{{ end }}){{ end }}", "Go template for the message")
	flag.Parse()
	viper.BindPFlags(flag.CommandLine)

	replacer := strings.NewReplacer(".", "_")
	viper.SetEnvKeyReplacer(replacer)
	viper.AutomaticEnv()

	initTpl()

	client := &http.Client{}

	http.Handle("/metrics", promhttp.Handler())

	http.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
		decoder := json.NewDecoder(r.Body)
		msg := &alertmanagerWebhook.Message{}

		if err := decoder.Decode(msg); err != nil {
			alertsErrors.WithLabelValues("invalid input").Inc()
			http.Error(w, err.Error(), 400)
			return
		}

		topic, message, err := formatAlerts(msg)
		if err != nil {
			alertsErrors.WithLabelValues("template error").Inc()
			http.Error(w, err.Error(), 500)
			return
		}

		log.Println("Alert", topic)

		data := url.Values{}
		data.Set("type", "stream")
		data.Set("to", viper.GetString("stream"))
		data.Set("topic", topic)
		data.Set("content", message)

		req, err := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/messages", viper.GetString("url")), strings.NewReader(data.Encode()))
		if err != nil {
			alertsErrors.WithLabelValues("request creation error").Inc()
			http.Error(w, err.Error(), 500)
			return
		}

		req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
		req.SetBasicAuth(viper.GetString("username"), viper.GetString("password"))

		_, err = client.Do(req)
		if err != nil {
			alertsErrors.WithLabelValues("request error").Inc()
			http.Error(w, err.Error(), 500)
			return
		}

		alertsSent.WithLabelValues(topic).Inc()

		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "ok")
	})

	port := viper.GetInt("port")
	listen := fmt.Sprintf(":%d", port)
	log.Println("Starting server", listen)
	http.ListenAndServe(listen, nil)
}
