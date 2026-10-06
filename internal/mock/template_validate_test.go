package mock

import (
	"strings"
	"testing"
)

func TestValidateTemplateCatchesParseErrors(t *testing.T) {
	good := []string{"", "plain text", `{"id":"{{.Request.Body.id}}"}`, `{{ fake "uuid" }}`, `{{ if .Request.Body }}x{{ end }}`, `{{ counter "n" }} {{ csv "email" }} {{ now | date "2006" }}`}
	for _, src := range good {
		if err := ValidateTemplate(src); err != nil {
			t.Errorf("%q should be valid: %v", src, err)
		}
	}
	bad := map[string]string{
		`{{ if }`:            "",
		`{{ .Request.Body`:   "",
		`{{ if .X }}no end`:  "",
		`{{ nosuchfunc 1 }}`: "nosuchfunc",
		`{{ end }}`:          "",
	}
	for src, want := range bad {
		err := ValidateTemplate(src)
		if err == nil {
			t.Errorf("%q should be rejected", src)
			continue
		}
		if want != "" && !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error %q should mention %q", src, err, want)
		}
	}
}

func TestValidateDefinitionTemplatesNamesTheBrokenField(t *testing.T) {
	d := &Definition{
		ProtocolType: "rest", Method: "GET", PathPattern: "/x",
		Response: ResponseTemplate{BodyTemplate: "ok"},
		ResponseRules: []ResponseRule{
			{Response: ResponseTemplate{BodyTemplate: "fine"}},
			{Response: ResponseTemplate{BodyTemplate: "{{ if }"}},
		},
	}
	err := ValidateDefinitionTemplates(d)
	if err == nil || !strings.Contains(err.Error(), "responseRules[1].response.bodyTemplate") {
		t.Fatalf("expected an error naming responseRules[1].response.bodyTemplate, got %v", err)
	}

	cases := map[string]*Definition{
		"response.bodyTemplate":                       {Response: ResponseTemplate{BodyTemplate: "{{"}},
		"scenario.steps[0].bodyTemplate":              {Scenario: &ScenarioConfig{Steps: []ResponseTemplate{{BodyTemplate: "{{ if }"}}}},
		"weighted.responses[0].response.bodyTemplate": {Weighted: &WeightedConfig{Responses: []WeightedResponse{{Weight: 1, Response: ResponseTemplate{BodyTemplate: "{{"}}}}},
		"validationErrorResponse.bodyTemplate":        {ValidationErrorResponse: &ResponseTemplate{BodyTemplate: "{{"}},
		"asyncConfig.callbackBodyTemplate":            {AsyncConfig: &AsyncConfig{CallbackBodyTemplate: "{{"}},
		"asyncConfig.ackResponse.bodyTemplate":        {AsyncConfig: &AsyncConfig{AckResponse: ResponseTemplate{BodyTemplate: "{{"}}},
		"tcp.interactions[0].response":                {TCP: &TCPConfig{Interactions: []TCPInteraction{{Response: "{{"}}}},
		"tcp.defaultResponse":                         {TCP: &TCPConfig{DefaultResponse: "{{"}},
		"ws.onConnectMessage":                         {WS: &WSConfig{OnConnectMessage: "{{"}},
		"ws.interactions[0].response":                 {WS: &WSConfig{Interactions: []WSInteraction{{Response: "{{"}}}},
		"mqtt.rules[0].replyPayload":                  {MQTT: &MQTTConfig{Rules: []MQTTRule{{ReplyPayload: "{{"}}}},
		"kafka.rules[0].replyPayload":                 {Kafka: &KafkaConfig{Rules: []KafkaRule{{ReplyPayload: "{{"}}}},
		"smpp.rules[0].replyMessage":                  {SMPP: &SMPPConfig{Rules: []SMPPRule{{ReplyMessage: "{{"}}}},
		"jms.rules[0].replyPayload":                   {JMS: &JMSConfig{Rules: []JMSRule{{ReplyPayload: "{{"}}}},
	}
	for field, def := range cases {
		err := ValidateDefinitionTemplates(def)
		if err == nil || !strings.Contains(err.Error(), field) {
			t.Errorf("%s: expected an error naming it, got %v", field, err)
		}
	}
	if err := ValidateDefinitionTemplates(&Definition{Response: ResponseTemplate{BodyTemplate: `{"a":"{{ fake "uuid" }}"}`}}); err != nil {
		t.Errorf("valid definition rejected: %v", err)
	}
}
