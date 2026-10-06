package httpengine

import (
	"log"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/mock"
)

const jsonContentType = "application/json"

func (e *Engine) serveRest(w http.ResponseWriter, r *http.Request, def *mock.Definition) {
	bodyBytes, rawBody, ok := readRequestBody(w, r)
	if !ok {
		return
	}

	pathParams := map[string]string{}
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		for i, key := range rctx.URLParams.Keys {
			pathParams[key] = rctx.URLParams.Values[i]
		}
	}

	// Deliberately bypasses runValidation/Fault entirely: a proxy mock
	// returns a real upstream's real response verbatim (that's the whole
	// point of record & replay), so validating the request or injecting
	// chaos into what comes back would fight the "point this at the real
	// API" purpose rather than serve it.
	if def.Mode == "proxy" && def.Proxy != nil {
		e.serveProxy(w, r, rawBody, def)
		return
	}

	cw := newCapturingWriter(w)
	start := time.Now()
	defer e.logInboundHit(cw, r, def, bodyBytes, start)
	w = cw

	reqCtx := mock.BuildRequestContext(r, bodyBytes, pathParams)

	if e.runValidation(w, def, reqCtx) {
		return
	}

	if def.Mode == "async" && def.AsyncConfig != nil {
		e.serveAsync(w, reqCtx, def, jsonContentType)
		return
	}

	if def.Scenario != nil && len(def.Scenario.Steps) > 0 {
		e.serveScenario(w, r, reqCtx, def, jsonContentType)
		return
	}

	e.writeTemplatedResponse(w, mock.SelectResponse(def, reqCtx), reqCtx, def.Fault, def.ID)
}

// serveScenario advances (and serves) this session's step in def.Scenario.
// Scenario and ResponseRules are mutually exclusive per mock (see
// mock.Definition.Scenario's doc comment) — content-based and sequence-
// based branching are two distinct mechanisms, not composed, in v1. Shared
// by REST, SOAP, and GraphQL (see serveAsync's identical defaultContentType
// rationale — a SOAP scenario step's response is still XML by default).
func (e *Engine) serveScenario(w http.ResponseWriter, r *http.Request, reqCtx mock.RequestContext, def *mock.Definition, defaultContentType string) {
	if e.scenarioStepper == nil {
		log.Printf("airmock: mock %q has a scenario but no scenario stepper is configured; serving step 0", def.ID)
		e.writeTemplatedResponseWithDefaultContentType(w, def.Scenario.Steps[0], reqCtx, defaultContentType, def.Fault, def.ID)
		return
	}

	sessionKey := resolveSessionKey(def.Scenario, r, reqCtx)
	step, err := e.scenarioStepper.AdvanceScenarioStep(def.ID, sessionKey, len(def.Scenario.Steps), def.Scenario.Loop)
	if err != nil {
		http.Error(w, "advance scenario: "+err.Error(), http.StatusInternalServerError)
		return
	}
	e.writeTemplatedResponseWithDefaultContentType(w, def.Scenario.Steps[step], reqCtx, defaultContentType, def.Fault, def.ID)
}

// resolveSessionKey attributes a request to a scenario session per
// ScenarioConfig.SessionKeyMode, defaulting to the client's remote address
// so "the same client" works out of the box with no configuration.
func resolveSessionKey(cfg *mock.ScenarioConfig, r *http.Request, reqCtx mock.RequestContext) string {
	switch cfg.SessionKeyMode {
	case "header":
		return reqCtx.Header[cfg.SessionKeyField]
	case "query":
		return reqCtx.Query[cfg.SessionKeyField]
	case "body":
		value, _, _ := mock.ExtractField("body."+cfg.SessionKeyField, reqCtx)
		if s, ok := value.(string); ok {
			return s
		}
		return ""
	default: // "ip"
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			return r.RemoteAddr
		}
		return host
	}
}

// serveAsync writes the immediate ack response, then schedules the later
// callback as a durable job (see internal/mock/async.go) rather than firing
// it from a bare goroutine, so delivery survives a process restart. Shared
// by REST, SOAP, and GraphQL — defaultContentType lets each caller keep its
// own protocol's usual ack content type (SOAP's ack is still XML, not JSON)
// when the mock's AckResponse doesn't set its own Content-Type header.
func (e *Engine) serveAsync(w http.ResponseWriter, reqCtx mock.RequestContext, def *mock.Definition, defaultContentType string) {
	cfg := def.AsyncConfig

	target, err := mock.ResolveCallbackTarget(cfg, reqCtx) // a URL for the http channel, an email address for the email channel
	if err != nil {
		http.Error(w, "resolve callback target: "+err.Error(), http.StatusBadRequest)
		return
	}

	subjectTemplate, bodyTemplate, _, terr := mock.ResolveEmailContent(cfg, e.emailTemplates)
	if terr != nil {
		log.Printf("airmock: mock %q references email template %q which failed to resolve, falling back to its own inline subject/body: %v", def.ID, cfg.EmailTemplateID, terr)
		subjectTemplate, bodyTemplate = cfg.EmailSubjectTemplate, cfg.CallbackBodyTemplate
	}

	payload, err := mock.RenderBody(bodyTemplate, reqCtx, mock.RenderOptions{OwnerID: def.ID, DynamicValues: e.dynamicValues})
	if err != nil {
		http.Error(w, "callback body template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	payload = mock.DefaultCallbackPayload(cfg, bodyTemplate, payload)
	subject := ""
	if cfg.CallbackChannel == "email" {
		if subject, err = mock.RenderBody(subjectTemplate, reqCtx, mock.RenderOptions{OwnerID: def.ID, DynamicValues: e.dynamicValues}); err != nil {
			http.Error(w, "email subject template error: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	e.writeTemplatedResponseWithDefaultContentType(w, cfg.AckResponse, reqCtx, defaultContentType, def.Fault, def.ID)

	if e.scheduler == nil {
		log.Printf("airmock: mock %q is async but no callback scheduler is configured; callback dropped", def.ID)
		return
	}

	method := cfg.CallbackMethod
	if method == "" {
		method = http.MethodPost
	}
	err = e.scheduler.ScheduleCallback(def.ID, mock.CallbackRequest{
		TargetURL:   target,
		Method:      method,
		Headers:     cfg.CallbackHeaders,
		Payload:     payload,
		DelayMs:     cfg.CallbackDelayMs,
		MaxAttempts: cfg.MaxAttempts,
		Channel:     cfg.CallbackChannel,
		Subject:     subject,
	})
	if err != nil {
		log.Printf("airmock: failed to schedule callback for mock %q: %v", def.ID, err)
	}
}

func (e *Engine) writeTemplatedResponse(w http.ResponseWriter, resp mock.ResponseTemplate, reqCtx mock.RequestContext, fault *mock.FaultConfig, ownerID string) {
	e.writeTemplatedResponseWithDefaultContentType(w, resp, reqCtx, jsonContentType, fault, ownerID)
}

// writeTemplatedResponseWithDefaultContentType lets non-REST protocols
// (SOAP's text/xml, GraphQL's application/json-but-explicit) default the
// Content-Type header to something other than application/json when the
// mock author didn't set one explicitly, without duplicating the render/
// write logic. Every response path funnels through here, which is also
// why fault injection lives at the top of this one function rather than
// in each caller — a mock is flaky the same way no matter which of sync,
// async-ack, or scenario-step response it's about to send.
func (e *Engine) writeTemplatedResponseWithDefaultContentType(w http.ResponseWriter, resp mock.ResponseTemplate, reqCtx mock.RequestContext, defaultContentType string, fault *mock.FaultConfig, ownerID string) {
	switch mock.RollFault(fault) {
	case "timeout":
		simulateTimeout(w)
		return
	case "error":
		w.WriteHeader(mock.PickErrorStatus(fault))
		return
	}
	if fault != nil && fault.LatencyJitterMs > 0 {
		time.Sleep(mock.RandomJitter(fault.LatencyJitterMs))
	}

	if resp.DelayMs > 0 {
		time.Sleep(time.Duration(resp.DelayMs) * time.Millisecond)
	}

	body, err := mock.RenderBody(resp.BodyTemplate, reqCtx, mock.RenderOptions{OwnerID: ownerID, DynamicValues: e.dynamicValues})
	if err != nil {
		http.Error(w, "template render error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	for k, v := range resp.Headers {
		w.Header().Set(k, v)
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", defaultContentType)
	}

	statusCode := resp.StatusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	w.WriteHeader(statusCode)
	w.Write([]byte(body))
}

// simulateTimeout drops the connection instead of responding — the most
// realistic way to simulate "the server timed out" without this mock
// process actually hanging a goroutine open for minutes. The client sees
// a connection-reset/EOF, exactly what a real timeout looks like from its
// side. Falls back to doing nothing (no status line ever written) if the
// underlying ResponseWriter doesn't support hijacking.
func simulateTimeout(w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		return
	}
	conn.Close()
}
