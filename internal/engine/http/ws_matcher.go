package httpengine

import (
	"context"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"nhooyr.io/websocket"

	"github.com/addictedabhi/airmock/internal/hitlog"
	"github.com/addictedabhi/airmock/internal/mock"
	sessreg "github.com/addictedabhi/airmock/internal/session"
)

// wsSession wraps one accepted connection with its own write mutex —
// SendToSession (an operator-triggered write from the admin API, running on
// a completely different goroutine than this connection's own read/respond
// loop) must never race with that loop's own conn.Write calls: nhooyr's
// websocket.Conn.Write is not safe for concurrent use. reg/mockID/regID let
// writeText bump the registry's own outbound counter itself, the same
// pattern mqttengine's session.writeRaw uses, so every call site doesn't
// need to separately track and pass its own session id.
type wsSession struct {
	conn    *websocket.Conn
	writeMu sync.Mutex

	reg    *sessreg.Registry[*wsSession]
	mockID string
	regID  string
}

func (s *wsSession) writeText(ctx context.Context, payload []byte) error {
	s.writeMu.Lock()
	err := s.conn.Write(ctx, websocket.MessageText, payload)
	s.writeMu.Unlock()
	if err == nil {
		s.reg.Touch(s.mockID, s.regID, 0, 1)
	}
	return err
}

// serveWS upgrades the connection, optionally sends OnConnectMessage, then
// loops reading text messages and answering each via def.WS's interactions
// until the client disconnects or an interaction says to close.
func (e *Engine) serveWS(w http.ResponseWriter, r *http.Request, def *mock.Definition) {
	pathParams := wsPathParams(r)

	// Validation runs against the handshake request (query/header/path
	// params — a WS upgrade GET has no body, so a body.* rule has nothing
	// to check) BEFORE accepting the upgrade: runValidation writes a normal
	// HTTP error response, which is only possible pre-upgrade — once
	// websocket.Accept succeeds the connection is no longer plain HTTP and
	// there's no way to reject it with an HTTP status. Previously this was
	// never called at all, so a WS mock's Validation rules silently did
	// nothing no matter what was configured.
	handshakeCtx := mock.BuildRequestContext(r, nil, pathParams)
	if e.runValidation(w, def, handshakeCtx) {
		return
	}

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx := context.Background()

	sess := &wsSession{conn: conn, reg: e.sessReg, mockID: def.ID}
	regID := e.sessReg.Add(def.ID, "ws", sess, r.RemoteAddr, nil)
	sess.regID = regID
	defer e.sessReg.Remove(def.ID, regID)

	cfg := def.WS
	if !e.sendWSOnConnectMessage(ctx, sess, def, cfg, handshakeCtx) {
		return
	}

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return // client disconnected, or the previous iteration already closed
		}
		e.sessReg.Touch(def.ID, regID, 1, 0)
		if !e.handleWSMessage(ctx, sess, r, def, pathParams, data) {
			return
		}
	}
}

// handleWSMessage processes one received message — match, render, write,
// log, and (if the matched interaction is async) schedule its callback —
// reporting whether the caller should keep reading (false means close).
// Split out of serveWS purely to keep that function's cognitive complexity
// down as a flat "upgrade, then loop" shape.
func (e *Engine) handleWSMessage(ctx context.Context, sess *wsSession, r *http.Request, def *mock.Definition, pathParams map[string]string, data []byte) bool {
	start := time.Now()
	reqCtx := mock.BuildRequestContext(r, data, pathParams)
	cfg := def.WS

	response, closeAfter, matched := evaluateWSInteractions(cfg, string(data), reqCtx)

	// def.Fault previously did nothing at all for WS mocks — every other
	// protocol (REST/SOAP/GraphQL/TCP/SMTP/MQTT/FTP) applies it, but WS was
	// missed, leaving Fault a silently-inert config on the one remaining
	// protocol. "timeout" and "error" both drop the connection with no
	// response, matching TCP/SMTP: a WS interaction's response goes
	// directly back to the same connected client (unlike MQTT's publish,
	// which broadcasts to OTHER subscribers, hence MQTT's different choice
	// of suppressing just the reply instead of dropping the connection) —
	// there's no generic status-code concept for a raw WS message to
	// substitute "error" with the way HTTP does either.
	faulted := response != "" && isDropFault(mock.RollFault(def.Fault))
	if !faulted && response != "" && def.Fault != nil && def.Fault.LatencyJitterMs > 0 {
		time.Sleep(mock.RandomJitter(def.Fault.LatencyJitterMs))
	}

	var rendered string
	wrote := true
	switch {
	case faulted:
		wrote = false
	case response != "":
		var err error
		rendered, err = mock.RenderBody(response, reqCtx, mock.RenderOptions{OwnerID: def.ID, DynamicValues: e.dynamicValues})
		if err != nil {
			log.Printf("airmock: ws mock %q: response template error: %v", def.ID, err)
			rendered = ""
		} else if sess.writeText(ctx, []byte(rendered)) != nil {
			wrote = false
		}
	}
	e.recordWSHit(def, string(data), rendered, start)

	// Scheduled regardless of `wrote`: fault injection only ever affects
	// the immediate in-connection response, never an Async interaction's
	// separate, out-of-band webhook/email delivery — the same fix applied
	// to tcpengine's handleTCPLine for the identical class of bug (fault
	// dropping the response used to also silently drop the callback there).
	if matched != nil && matched.Async != nil {
		e.scheduleWSAsyncCallback(def, matched.Async, reqCtx)
	}
	if !wrote {
		return false
	}
	return !closeAfter
}

// isDropFault reports whether RollFault's result means "drop the
// connection without responding" — true for both "timeout" and "error",
// which WS treats identically (see handleWSMessage's comment).
func isDropFault(fault string) bool {
	return fault == "timeout" || fault == "error"
}

// scheduleWSAsyncCallback mirrors tcpengine's scheduleTCPAsyncCallback for a
// WSInteraction that matched with Async set — the same HTTP-webhook-or-
// email delivery any REST/TCP async mock uses, just triggered by a
// WebSocket message. Unlike TCP's line-only RequestContext, reqCtx here
// already carries the handshake's real Query/Header/PathParams (built by
// mock.BuildRequestContext from the original upgrade request), so a
// callback target can be extracted from any of those, not just the
// message body.
func (e *Engine) scheduleWSAsyncCallback(def *mock.Definition, cfg *mock.AsyncConfig, reqCtx mock.RequestContext) {
	if e.scheduler == nil {
		log.Printf("airmock: ws mock %q interaction is async but no callback scheduler is configured; callback dropped", def.ID)
		return
	}

	target, err := mock.ResolveCallbackTarget(cfg, reqCtx)
	if err != nil {
		log.Printf("airmock: ws mock %q: resolve callback target: %v", def.ID, err)
		return
	}

	subjectTemplate, bodyTemplate, _, terr := mock.ResolveEmailContent(cfg, e.emailTemplates)
	if terr != nil {
		log.Printf("airmock: ws mock %q references email template %q which failed to resolve, falling back to its own inline subject/body: %v", def.ID, cfg.EmailTemplateID, terr)
		subjectTemplate, bodyTemplate = cfg.EmailSubjectTemplate, cfg.CallbackBodyTemplate
	}

	payload, err := mock.RenderBody(bodyTemplate, reqCtx, mock.RenderOptions{OwnerID: def.ID, DynamicValues: e.dynamicValues})
	if err != nil {
		log.Printf("airmock: ws mock %q: callback body template error: %v", def.ID, err)
		return
	}
	payload = mock.DefaultCallbackPayload(cfg, bodyTemplate, payload)
	subject := ""
	if cfg.CallbackChannel == "email" {
		if subject, err = mock.RenderBody(subjectTemplate, reqCtx, mock.RenderOptions{OwnerID: def.ID, DynamicValues: e.dynamicValues}); err != nil {
			log.Printf("airmock: ws mock %q: email subject template error: %v", def.ID, err)
			return
		}
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
		log.Printf("airmock: failed to schedule ws callback for mock %q: %v", def.ID, err)
	}
}

func wsPathParams(r *http.Request) map[string]string {
	pathParams := map[string]string{}
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		for i, key := range rctx.URLParams.Keys {
			pathParams[key] = rctx.URLParams.Values[i]
		}
	}
	return pathParams
}

// sendWSOnConnectMessage renders and sends cfg.OnConnectMessage right after
// a successful upgrade, if configured, reporting whether the caller should
// keep going (false on either a failed write or a template error, matching
// serveWS's previous inline behavior).
func (e *Engine) sendWSOnConnectMessage(ctx context.Context, sess *wsSession, def *mock.Definition, cfg *mock.WSConfig, handshakeCtx mock.RequestContext) bool {
	if cfg.OnConnectMessage == "" {
		return true
	}
	msg, err := mock.RenderBody(cfg.OnConnectMessage, handshakeCtx, mock.RenderOptions{OwnerID: def.ID, DynamicValues: e.dynamicValues})
	if err != nil {
		log.Printf("airmock: ws mock %q: onConnectMessage template error: %v", def.ID, err)
		return true
	}
	return sess.writeText(ctx, []byte(msg)) == nil
}

// evaluateWSInteractions is first-match-wins over cfg.Interactions;
// DefaultResponse (never closing the connection) is the fallback. matched
// is nil for DefaultResponse/no-match, mirroring tcpengine's
// matchTCPInteraction, so the caller can act on its Async config.
func evaluateWSInteractions(cfg *mock.WSConfig, message string, reqCtx mock.RequestContext) (response string, closeAfter bool, matched *mock.WSInteraction) {
	for i := range cfg.Interactions {
		it := &cfg.Interactions[i]
		if !wsInteractionMatches(*it, message) {
			continue
		}
		return it.Response, it.CloseAfter, it
	}
	return cfg.DefaultResponse, false, nil
}

func wsInteractionMatches(it mock.WSInteraction, message string) bool {
	switch it.MatchType {
	case "exact":
		return message == it.Match
	case "regex":
		matched, err := regexp.MatchString(it.Match, message)
		return err == nil && matched
	default: // "contains"
		return strings.Contains(message, it.Match)
	}
}

func (e *Engine) recordWSHit(def *mock.Definition, requestBody, responseBody string, start time.Time) {
	if e.hitLogger == nil {
		return
	}
	err := e.hitLogger.Record(&hitlog.Entry{
		MockID:       def.ID,
		ProtocolType: "ws",
		Direction:    hitlog.DirectionInbound,
		Method:       http.MethodGet,
		Path:         def.PathPattern,
		RequestBody:  requestBody,
		ResponseBody: responseBody,
		LatencyMs:    time.Since(start).Milliseconds(),
	})
	if err != nil {
		log.Printf("airmock: failed to record ws hit for mock %q: %v", def.ID, err)
	}
}
