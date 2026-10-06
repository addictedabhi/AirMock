package mock

import (
	"fmt"
	"text/template"
)

// ValidateTemplate checks that src parses with the same function set the
// renderer uses (sprig, fake, counter, csv), so a typo such as "{{ if }" or
// an unknown function is reported when a mock is saved instead of failing
// every request that hits it. It only parses; whether the template's data
// access works depends on the request and is checked at render time.
func ValidateTemplate(src string) error {
	if src == "" {
		return nil
	}
	if _, err := template.New("body").Funcs(funcMap(RenderOptions{})).Parse(src); err != nil {
		return err
	}
	return nil
}

// ValidateDefinitionTemplates validates every template a mock carries and
// names the first broken one by its JSON field path.
func ValidateDefinitionTemplates(d *Definition) error {
	var firstErr error
	check := func(path, src string) {
		if firstErr != nil {
			return
		}
		if err := ValidateTemplate(src); err != nil {
			firstErr = fmt.Errorf("%s is not a valid template: %w", path, err)
		}
	}

	check("response.bodyTemplate", d.Response.BodyTemplate)
	for i, r := range d.ResponseRules {
		check(fmt.Sprintf("responseRules[%d].response.bodyTemplate", i), r.Response.BodyTemplate)
	}
	if d.Scenario != nil {
		for i, s := range d.Scenario.Steps {
			check(fmt.Sprintf("scenario.steps[%d].bodyTemplate", i), s.BodyTemplate)
		}
	}
	if d.Weighted != nil {
		for i, w := range d.Weighted.Responses {
			check(fmt.Sprintf("weighted.responses[%d].response.bodyTemplate", i), w.Response.BodyTemplate)
		}
	}
	if d.ValidationErrorResponse != nil {
		check("validationErrorResponse.bodyTemplate", d.ValidationErrorResponse.BodyTemplate)
	}
	checkAsync := func(prefix string, a *AsyncConfig) {
		if a == nil {
			return
		}
		check(prefix+".ackResponse.bodyTemplate", a.AckResponse.BodyTemplate)
		check(prefix+".callbackBodyTemplate", a.CallbackBodyTemplate)
		check(prefix+".emailSubjectTemplate", a.EmailSubjectTemplate)
	}
	checkAsync("asyncConfig", d.AsyncConfig)

	if d.TCP != nil {
		check("tcp.defaultResponse", d.TCP.DefaultResponse)
		for i, in := range d.TCP.Interactions {
			check(fmt.Sprintf("tcp.interactions[%d].response", i), in.Response)
			checkAsync(fmt.Sprintf("tcp.interactions[%d].async", i), in.Async)
		}
	}
	if d.WS != nil {
		check("ws.onConnectMessage", d.WS.OnConnectMessage)
		check("ws.defaultResponse", d.WS.DefaultResponse)
		for i, in := range d.WS.Interactions {
			check(fmt.Sprintf("ws.interactions[%d].response", i), in.Response)
			checkAsync(fmt.Sprintf("ws.interactions[%d].async", i), in.Async)
		}
	}
	if d.MQTT != nil {
		for i, r := range d.MQTT.Rules {
			check(fmt.Sprintf("mqtt.rules[%d].replyPayload", i), r.ReplyPayload)
		}
	}
	if d.Kafka != nil {
		for i, r := range d.Kafka.Rules {
			check(fmt.Sprintf("kafka.rules[%d].replyPayload", i), r.ReplyPayload)
		}
	}
	if d.SMPP != nil {
		for i, r := range d.SMPP.Rules {
			check(fmt.Sprintf("smpp.rules[%d].replyMessage", i), r.ReplyMessage)
		}
	}
	if d.JMS != nil {
		for i, r := range d.JMS.Rules {
			check(fmt.Sprintf("jms.rules[%d].replyPayload", i), r.ReplyPayload)
		}
	}
	return firstErr
}
