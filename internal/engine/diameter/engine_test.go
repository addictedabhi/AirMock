package diameterengine

import (
	"sync"
	"testing"
	"time"

	"github.com/fiorix/go-diameter/v4/diam"
	"github.com/fiorix/go-diameter/v4/diam/avp"
	"github.com/fiorix/go-diameter/v4/diam/datatype"
	"github.com/fiorix/go-diameter/v4/diam/dict"
	"github.com/fiorix/go-diameter/v4/diam/sm"

	"github.com/addictedabhi/airmock/internal/hitlog"
	"github.com/addictedabhi/airmock/internal/mock"
)

type fakeHitLogger struct {
	mu      sync.Mutex
	entries []*hitlog.Entry
}

func (f *fakeHitLogger) Record(e *hitlog.Entry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, e)
	return nil
}

func (f *fakeHitLogger) all() []*hitlog.Entry {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*hitlog.Entry, len(f.entries))
	copy(out, f.entries)
	return out
}

func registerOnFreePort(t *testing.T, e *Engine, m *mock.Definition) string {
	t.Helper()
	m.Diameter.Port = 0
	if err := e.RegisterMock(m); err != nil {
		t.Fatalf("RegisterMock: %v", err)
	}
	ln, ok := e.listeners[m.ID]
	if !ok {
		t.Fatal("expected a listener to be registered")
	}
	return ln.Addr().String()
}

func newDef(name string, cfg *mock.DiameterConfig) *mock.Definition {
	return &mock.Definition{ID: "diameter-" + name, Name: name, ProtocolType: "diameter", Enabled: true, Diameter: cfg}
}

// dialClient binds a real github.com/fiorix/go-diameter client against
// addr — the exact "real client library completes a basic exchange" bar
// the mock is meant to clear, exercising the actual CER/CEA handshake this
// engine's underlying sm.StateMachine performs (not a hand-rolled fake
// client), plus a CCA handler so tests can observe the mock's answers the
// same idiomatic way the library's own examples do.
func dialClient(t *testing.T, addr string, onCCA diam.HandlerFunc) diam.Conn {
	t.Helper()
	cfg := &sm.Settings{
		OriginHost:  datatype.DiameterIdentity("test-client"),
		OriginRealm: datatype.DiameterIdentity("test.local"),
		VendorID:    99,
		ProductName: "airmock-test-client",
	}
	mux := sm.New(cfg)
	if onCCA != nil {
		mux.Handle("CCA", onCCA)
	}
	cli := &sm.Client{
		Dict:    dict.Default,
		Handler: mux,
		AuthApplicationID: []*diam.AVP{
			diam.NewAVP(avp.AuthApplicationID, avp.Mbit, 0, datatype.Unsigned32(4)), // RFC 4006 Credit Control
		},
	}
	conn, err := cli.Dial(addr)
	if err != nil {
		t.Fatalf("Dial (CER/CEA handshake): %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// sendCCR builds and sends a minimal Credit-Control-Request over an
// already-handshaken connection.
func sendCCR(t *testing.T, conn diam.Conn, sessionID string, ccRequestType int32) {
	t.Helper()
	m := diam.NewRequest(diam.CreditControl, 4, dict.Default)
	m.NewAVP(avp.SessionID, avp.Mbit, 0, datatype.UTF8String(sessionID))
	m.NewAVP(avp.OriginHost, avp.Mbit, 0, datatype.DiameterIdentity("test-client"))
	m.NewAVP(avp.OriginRealm, avp.Mbit, 0, datatype.DiameterIdentity("test.local"))
	m.NewAVP(avp.DestinationRealm, avp.Mbit, 0, datatype.DiameterIdentity(defaultOriginRealm))
	m.NewAVP(avp.CCRequestType, avp.Mbit, 0, datatype.Enumerated(ccRequestType))
	m.NewAVP(avp.CCRequestNumber, avp.Mbit, 0, datatype.Unsigned32(0))
	if _, err := m.WriteTo(conn); err != nil {
		t.Fatalf("send CCR: %v", err)
	}
}

func resultCodeOf(t *testing.T, m *diam.Message) uint32 {
	t.Helper()
	a, err := m.FindAVP(avp.ResultCode, 0)
	if err != nil {
		t.Fatalf("expected a Result-Code AVP: %v", err)
	}
	return uint32(a.Data.(datatype.Unsigned32))
}

func TestDiameterMockHandshakeAndCCA(t *testing.T) {
	e := New()
	def := newDef("handshake", &mock.DiameterConfig{})
	addr := registerOnFreePort(t, e, def)

	received := make(chan *diam.Message, 1)
	conn := dialClient(t, addr, func(c diam.Conn, m *diam.Message) { received <- m })

	sendCCR(t, conn, "session-1", 1)

	select {
	case m := <-received:
		if got := resultCodeOf(t, m); got != 2001 {
			t.Fatalf("expected Result-Code 2001, got %d", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for CCA")
	}
}

func TestDiameterMockRuleMatchesRequestTypeAndSessionID(t *testing.T) {
	e := New()
	def := newDef("rule-match", &mock.DiameterConfig{
		Rules: []mock.DiameterRule{
			{CCRequestType: 3, SessionIDMatch: "vip", MatchType: "contains", ResultCode: 5012},
		},
	})
	addr := registerOnFreePort(t, e, def)

	received := make(chan *diam.Message, 4)
	conn := dialClient(t, addr, func(c diam.Conn, m *diam.Message) { received <- m })

	// Doesn't match: wrong CCRequestType.
	sendCCR(t, conn, "vip-session", 1)
	// Doesn't match: right type, but session doesn't contain "vip".
	sendCCR(t, conn, "plain-session", 3)
	// Matches both.
	sendCCR(t, conn, "vip-session", 3)

	var codes []uint32
	for i := 0; i < 3; i++ {
		select {
		case m := <-received:
			codes = append(codes, resultCodeOf(t, m))
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for CCA %d/3", i+1)
		}
	}
	want := []uint32{2001, 2001, 5012}
	for i, w := range want {
		if codes[i] != w {
			t.Fatalf("CCA %d: expected Result-Code %d, got %d (all: %v)", i+1, w, codes[i], codes)
		}
	}
}

func TestDiameterMockDefaultsToSuccessWithNoRules(t *testing.T) {
	e := New()
	def := newDef("no-rules", &mock.DiameterConfig{})
	addr := registerOnFreePort(t, e, def)

	received := make(chan *diam.Message, 1)
	conn := dialClient(t, addr, func(c diam.Conn, m *diam.Message) { received <- m })

	sendCCR(t, conn, "any-session", 4)

	select {
	case m := <-received:
		if got := resultCodeOf(t, m); got != 2001 {
			t.Fatalf("expected default Result-Code 2001, got %d", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for CCA")
	}
}

func TestDiameterMockOriginIdentityIsConfigurable(t *testing.T) {
	e := New()
	def := newDef("custom-identity", &mock.DiameterConfig{
		OriginHost:  "pcrf.example.test",
		OriginRealm: "example.test",
	})
	addr := registerOnFreePort(t, e, def)

	received := make(chan *diam.Message, 1)
	conn := dialClient(t, addr, func(c diam.Conn, m *diam.Message) { received <- m })
	sendCCR(t, conn, "session-1", 1)

	select {
	case m := <-received:
		hostAVP, err := m.FindAVP(avp.OriginHost, 0)
		if err != nil {
			t.Fatalf("expected Origin-Host AVP: %v", err)
		}
		if got := string(hostAVP.Data.(datatype.DiameterIdentity)); got != "pcrf.example.test" {
			t.Fatalf("expected Origin-Host %q, got %q", "pcrf.example.test", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for CCA")
	}
}

func TestDiameterMockRecordsHits(t *testing.T) {
	e := New()
	logger := &fakeHitLogger{}
	e.SetHitLogger(logger)
	def := newDef("hit-logging", &mock.DiameterConfig{})
	addr := registerOnFreePort(t, e, def)

	received := make(chan *diam.Message, 1)
	conn := dialClient(t, addr, func(c diam.Conn, m *diam.Message) { received <- m })
	sendCCR(t, conn, "session-log", 1)

	select {
	case <-received:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for CCA")
	}

	deadline := time.Now().Add(2 * time.Second)
	for len(logger.all()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	entries := logger.all()
	if len(entries) != 1 {
		t.Fatalf("expected 1 hit-log entry, got %d", len(entries))
	}
	if entries[0].Path != "session-log" || entries[0].ResponseBody != "CCA Result-Code=2001" {
		t.Fatalf("unexpected entry: %+v", entries[0])
	}
}

func TestDiameterMockFaultSuppressesAnswer(t *testing.T) {
	e := New()
	def := newDef("fault-suppress", &mock.DiameterConfig{})
	def.Fault = &mock.FaultConfig{ErrorRatePercent: 100, ErrorStatusCodes: []int{500}}
	addr := registerOnFreePort(t, e, def)

	received := make(chan *diam.Message, 1)
	conn := dialClient(t, addr, func(c diam.Conn, m *diam.Message) { received <- m })
	sendCCR(t, conn, "session-1", 1)

	select {
	case m := <-received:
		t.Fatalf("expected no CCA under a 100%% fault, got one: %+v", m)
	case <-time.After(300 * time.Millisecond):
		// correct: fault suppressed the answer
	}
}

func TestReRegisteringADiameterMockClosesAlreadyConnectedConns(t *testing.T) {
	e := New()
	def := newDef("re-register", &mock.DiameterConfig{})
	addr := registerOnFreePort(t, e, def)

	conn := dialClient(t, addr, nil)

	deadline := time.Now().Add(2 * time.Second)
	for len(e.ListSessions(def.ID)) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	registerOnFreePort(t, e, def) // re-register the same mock ID

	closed := make(chan struct{})
	go func() {
		buf := make([]byte, 1)
		conn.Connection().SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err := conn.Connection().Read(buf); err != nil {
			close(closed)
		}
	}()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("expected the old connection to be closed after re-registering the mock")
	}
}

func TestUnregisteringADiameterMockClosesAlreadyConnectedConns(t *testing.T) {
	e := New()
	def := newDef("unregister", &mock.DiameterConfig{})
	addr := registerOnFreePort(t, e, def)

	conn := dialClient(t, addr, nil)

	deadline := time.Now().Add(2 * time.Second)
	for len(e.ListSessions(def.ID)) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if err := e.UnregisterMock(def.ID); err != nil {
		t.Fatalf("UnregisterMock: %v", err)
	}

	buf := make([]byte, 1)
	conn.Connection().SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Connection().Read(buf); err == nil {
		t.Fatal("expected the connection to be closed after unregistering the mock")
	}
}

func TestUnregisterDiameterMockClosesListener(t *testing.T) {
	e := New()
	def := newDef("unregister-listener", &mock.DiameterConfig{})
	addr := registerOnFreePort(t, e, def)

	if err := e.UnregisterMock(def.ID); err != nil {
		t.Fatalf("UnregisterMock: %v", err)
	}
	if _, err := diam.Dial(addr, nil, nil); err == nil {
		t.Fatal("expected dial to fail after unregistering the mock")
	}
}

func TestRegisterDiameterMockDisabledDoesNotListen(t *testing.T) {
	e := New()
	def := newDef("disabled", &mock.DiameterConfig{Port: 0})
	def.Enabled = false
	if err := e.RegisterMock(def); err != nil {
		t.Fatalf("RegisterMock: %v", err)
	}
	if _, ok := e.listeners[def.ID]; ok {
		t.Fatal("expected no listener for a disabled mock")
	}
}

// TestDiameterSessionsListCapturesPeerIdentityAndCloses covers the admin
// "Connected sessions" API surface: a handshaken peer shows up in
// ListSessions with its real Origin-Host/Realm captured into Meta once its
// first CCR arrives, and CloseSession forcibly drops the connection.
// Diameter has no SendToSession (see ListSessions' own doc comment).
func TestDiameterSessionsListCapturesPeerIdentityAndCloses(t *testing.T) {
	e := New()
	def := newDef("sessions", &mock.DiameterConfig{})
	addr := registerOnFreePort(t, e, def)

	conn := dialClient(t, addr, nil)

	deadline := time.Now().Add(2 * time.Second)
	for len(e.ListSessions(def.ID)) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	sessions := e.ListSessions(def.ID)
	if len(sessions) != 1 {
		t.Fatalf("expected 1 connected session, got %d", len(sessions))
	}
	if sessions[0].Protocol != "diameter" {
		t.Fatalf("expected protocol %q, got %q", "diameter", sessions[0].Protocol)
	}
	if sessions[0].Meta["originHost"] != "" {
		t.Fatalf("expected no Origin-Host captured before any CCR, got %+v", sessions[0].Meta)
	}

	sendCCR(t, conn, "sess-1", 1)

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sessions = e.ListSessions(def.ID)
		if sessions[0].Meta["originHost"] != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if sessions[0].Meta["originHost"] != "test-client" || sessions[0].Meta["originRealm"] != "test.local" {
		t.Fatalf("expected the CCR's Origin-Host/Realm captured in Meta, got %+v", sessions[0].Meta)
	}
	if sessions[0].MessagesIn == 0 || sessions[0].MessagesOut == 0 {
		t.Fatalf("expected the CCR/CCA exchange to have bumped both counters, got %+v", sessions[0])
	}

	if err := e.CloseSession(def.ID, sessions[0].ID); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	buf := make([]byte, 1)
	conn.Connection().SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Connection().Read(buf); err == nil {
		t.Fatal("expected the connection to be closed after CloseSession")
	}

	if err := e.CloseSession(def.ID, "no-such-session"); err == nil {
		t.Fatal("expected an error closing a session id that doesn't exist")
	}
}

// ---- Multiple-Services-Credit-Control / granted units ----

type msccSpec struct {
	ratingGroup, serviceID uint32
}

func sendCCRWith(t *testing.T, conn diam.Conn, sessionID string, ccRequestType int32, subscriptionID string, msccs ...msccSpec) {
	t.Helper()
	m := diam.NewRequest(diam.CreditControl, 4, dict.Default)
	m.NewAVP(avp.SessionID, avp.Mbit, 0, datatype.UTF8String(sessionID))
	m.NewAVP(avp.OriginHost, avp.Mbit, 0, datatype.DiameterIdentity("test-client"))
	m.NewAVP(avp.OriginRealm, avp.Mbit, 0, datatype.DiameterIdentity("test.local"))
	m.NewAVP(avp.DestinationRealm, avp.Mbit, 0, datatype.DiameterIdentity(defaultOriginRealm))
	m.NewAVP(avp.CCRequestType, avp.Mbit, 0, datatype.Enumerated(ccRequestType))
	m.NewAVP(avp.CCRequestNumber, avp.Mbit, 0, datatype.Unsigned32(0))
	if subscriptionID != "" {
		m.NewAVP(avp.SubscriptionID, avp.Mbit, 0, &diam.GroupedAVP{AVP: []*diam.AVP{
			diam.NewAVP(avp.SubscriptionIDType, avp.Mbit, 0, datatype.Enumerated(0)),
			diam.NewAVP(avp.SubscriptionIDData, avp.Mbit, 0, datatype.UTF8String(subscriptionID)),
		}})
	}
	for _, s := range msccs {
		m.NewAVP(avp.MultipleServicesCreditControl, avp.Mbit, 0, &diam.GroupedAVP{AVP: []*diam.AVP{
			diam.NewAVP(avp.RatingGroup, avp.Mbit, 0, datatype.Unsigned32(s.ratingGroup)),
			diam.NewAVP(avp.ServiceIdentifier, avp.Mbit, 0, datatype.Unsigned32(s.serviceID)),
			diam.NewAVP(avp.RequestedServiceUnit, avp.Mbit, 0, &diam.GroupedAVP{AVP: []*diam.AVP{
				diam.NewAVP(avp.CCTotalOctets, avp.Mbit, 0, datatype.Unsigned64(1000000)),
			}}),
		}})
	}
	if _, err := m.WriteTo(conn); err != nil {
		t.Fatalf("send CCR: %v", err)
	}
}

// answerMSCCs returns each MSCC in a CCA as a flat name→value map plus the
// raw inner AVPs, for assertions.
type gotMSCC struct {
	ratingGroup, serviceID, validity *uint32
	totalOctets                      *uint64
	ccTime                           *uint32
	ssUnits                          *uint64
	hasGSU                           bool
	finalUnitAction                  *int32
}

func parseAnswerMSCCs(t *testing.T, m *diam.Message) []gotMSCC {
	t.Helper()
	var out []gotMSCC
	for _, a := range m.AVP {
		if a.Code != avp.MultipleServicesCreditControl {
			continue
		}
		g := gotMSCC{}
		grp := a.Data.(*diam.GroupedAVP)
		for _, in := range grp.AVP {
			switch in.Code {
			case avp.RatingGroup:
				v := uint32(in.Data.(datatype.Unsigned32))
				g.ratingGroup = &v
			case avp.ServiceIdentifier:
				v := uint32(in.Data.(datatype.Unsigned32))
				g.serviceID = &v
			case avp.ValidityTime:
				v := uint32(in.Data.(datatype.Unsigned32))
				g.validity = &v
			case avp.GrantedServiceUnit:
				g.hasGSU = true
				for _, u := range in.Data.(*diam.GroupedAVP).AVP {
					switch u.Code {
					case avp.CCTotalOctets:
						v := uint64(u.Data.(datatype.Unsigned64))
						g.totalOctets = &v
					case avp.CCTime:
						v := uint32(u.Data.(datatype.Unsigned32))
						g.ccTime = &v
					case avp.CCServiceSpecificUnits:
						v := uint64(u.Data.(datatype.Unsigned64))
						g.ssUnits = &v
					}
				}
			case avp.FinalUnitIndication:
				for _, u := range in.Data.(*diam.GroupedAVP).AVP {
					if u.Code == avp.FinalUnitAction {
						v := int32(u.Data.(datatype.Enumerated))
						g.finalUnitAction = &v
					}
				}
			}
		}
		out = append(out, g)
	}
	return out
}

func u64(v uint64) *uint64 { return &v }
func u32(v uint32) *uint32 { return &v }

func waitCCA(t *testing.T, ch chan *diam.Message) *diam.Message {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for CCA")
		return nil
	}
}

func TestDiameterInitialWithMSCCGetsGrantedServiceUnits(t *testing.T) {
	e := New()
	def := newDef("gsu", &mock.DiameterConfig{Rules: []mock.DiameterRule{{
		CCRequestType: 1, GrantedTotalOctets: u64(5242880), GrantedTime: u32(3600),
		GrantedServiceSpecificUnits: u64(42), ValidityTime: u32(600),
	}}})
	addr := registerOnFreePort(t, e, def)
	received := make(chan *diam.Message, 2)
	conn := dialClient(t, addr, func(c diam.Conn, m *diam.Message) { received <- m })

	sendCCRWith(t, conn, "s1", 1, "", msccSpec{ratingGroup: 100, serviceID: 7})
	ms := parseAnswerMSCCs(t, waitCCA(t, received))
	if len(ms) != 1 {
		t.Fatalf("expected one MSCC in the CCA, got %d", len(ms))
	}
	g := ms[0]
	if g.ratingGroup == nil || *g.ratingGroup != 100 || g.serviceID == nil || *g.serviceID != 7 {
		t.Fatalf("expected Rating-Group 100 / Service-Identifier 7 echoed, got %+v", g)
	}
	if !g.hasGSU || g.totalOctets == nil || *g.totalOctets != 5242880 || g.ccTime == nil || *g.ccTime != 3600 || g.ssUnits == nil || *g.ssUnits != 42 {
		t.Fatalf("unexpected Granted-Service-Unit: %+v", g)
	}
	if g.validity == nil || *g.validity != 600 {
		t.Fatalf("expected Validity-Time 600, got %+v", g.validity)
	}
	if g.finalUnitAction != nil {
		t.Fatalf("did not expect a Final-Unit-Indication on this rule")
	}
}

func TestDiameterUpdateCanReturnFinalUnitIndication(t *testing.T) {
	e := New()
	def := newDef("fui", &mock.DiameterConfig{Rules: []mock.DiameterRule{
		{CCRequestType: 2, GrantedTotalOctets: u64(0), FinalUnitAction: "terminate"},
		{CCRequestType: 1, GrantedTotalOctets: u64(1048576)},
	}})
	addr := registerOnFreePort(t, e, def)
	received := make(chan *diam.Message, 2)
	conn := dialClient(t, addr, func(c diam.Conn, m *diam.Message) { received <- m })

	sendCCRWith(t, conn, "s1", 1, "", msccSpec{ratingGroup: 1})
	first := parseAnswerMSCCs(t, waitCCA(t, received))
	sendCCRWith(t, conn, "s1", 2, "", msccSpec{ratingGroup: 1})
	second := parseAnswerMSCCs(t, waitCCA(t, received))

	if len(first) != 1 || first[0].finalUnitAction != nil || first[0].totalOctets == nil || *first[0].totalOctets != 1048576 {
		t.Fatalf("initial answer wrong: %+v", first)
	}
	if len(second) != 1 || second[0].finalUnitAction == nil || *second[0].finalUnitAction != 0 {
		t.Fatalf("update should carry Final-Unit-Action TERMINATE(0), got %+v", second)
	}
	if second[0].totalOctets == nil || *second[0].totalOctets != 0 {
		t.Fatalf("explicit zero grant should be preserved, got %+v", second[0].totalOctets)
	}
}

func TestDiameterAnswersOneMSCCPerRequestMSCC(t *testing.T) {
	e := New()
	def := newDef("multi", &mock.DiameterConfig{Rules: []mock.DiameterRule{{GrantedTotalOctets: u64(100)}}})
	addr := registerOnFreePort(t, e, def)
	received := make(chan *diam.Message, 1)
	conn := dialClient(t, addr, func(c diam.Conn, m *diam.Message) { received <- m })

	sendCCRWith(t, conn, "s1", 1, "", msccSpec{10, 1}, msccSpec{20, 2})
	ms := parseAnswerMSCCs(t, waitCCA(t, received))
	if len(ms) != 2 || *ms[0].ratingGroup != 10 || *ms[1].ratingGroup != 20 || *ms[1].serviceID != 2 {
		t.Fatalf("expected two echoed MSCCs, got %+v", ms)
	}
}

func TestDiameterGrantWithoutRequestMSCCStillAnswersOneMSCC(t *testing.T) {
	e := New()
	def := newDef("nomscc", &mock.DiameterConfig{Rules: []mock.DiameterRule{{GrantedTime: u32(60)}}})
	addr := registerOnFreePort(t, e, def)
	received := make(chan *diam.Message, 1)
	conn := dialClient(t, addr, func(c diam.Conn, m *diam.Message) { received <- m })

	sendCCR(t, conn, "s1", 1)
	ms := parseAnswerMSCCs(t, waitCCA(t, received))
	if len(ms) != 1 || ms[0].ccTime == nil || *ms[0].ccTime != 60 || ms[0].ratingGroup != nil {
		t.Fatalf("expected a single MSCC with no echoed ids, got %+v", ms)
	}
}

func TestDiameterTerminationNeverCarriesGrant(t *testing.T) {
	e := New()
	def := newDef("term", &mock.DiameterConfig{Rules: []mock.DiameterRule{{GrantedTotalOctets: u64(100)}}})
	addr := registerOnFreePort(t, e, def)
	received := make(chan *diam.Message, 1)
	conn := dialClient(t, addr, func(c diam.Conn, m *diam.Message) { received <- m })

	sendCCRWith(t, conn, "s1", 3, "", msccSpec{1, 1})
	if ms := parseAnswerMSCCs(t, waitCCA(t, received)); len(ms) != 0 {
		t.Fatalf("a termination answer must not carry granted units, got %+v", ms)
	}
}

func TestDiameterRuleMatchesOnRatingGroupAndSubscriptionID(t *testing.T) {
	e := New()
	def := newDef("match", &mock.DiameterConfig{Rules: []mock.DiameterRule{
		{RatingGroup: 200, ResultCode: 4012},
		{SubscriptionIDMatch: "9198", MatchType: "contains", ResultCode: 5030},
	}})
	addr := registerOnFreePort(t, e, def)
	received := make(chan *diam.Message, 3)
	conn := dialClient(t, addr, func(c diam.Conn, m *diam.Message) { received <- m })

	sendCCRWith(t, conn, "s1", 1, "1234", msccSpec{ratingGroup: 200})    // rating group rule
	sendCCRWith(t, conn, "s2", 1, "919800001", msccSpec{ratingGroup: 1}) // subscription rule
	sendCCRWith(t, conn, "s3", 1, "5555", msccSpec{ratingGroup: 1})      // none -> default

	var got []uint32
	for i := 0; i < 3; i++ {
		got = append(got, resultCodeOf(t, waitCCA(t, received)))
	}
	want := []uint32{4012, 5030, 2001}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("answers %v, want %v", got, want)
		}
	}
}

func TestDiameterRuleDelayAndRuleLevelFault(t *testing.T) {
	e := New()
	def := newDef("delay", &mock.DiameterConfig{Rules: []mock.DiameterRule{
		{SessionIDMatch: "slow", DelayMs: 250},
		{SessionIDMatch: "drop", Fault: &mock.FaultConfig{ErrorRatePercent: 100, ErrorStatusCodes: []int{500}}},
	}})
	addr := registerOnFreePort(t, e, def)
	received := make(chan *diam.Message, 3)
	conn := dialClient(t, addr, func(c diam.Conn, m *diam.Message) { received <- m })

	start := time.Now()
	sendCCR(t, conn, "slow-1", 1)
	waitCCA(t, received)
	if d := time.Since(start); d < 240*time.Millisecond {
		t.Fatalf("expected the rule's 250ms delay, answered after %v", d)
	}

	sendCCR(t, conn, "drop-1", 1)
	select {
	case <-received:
		t.Fatal("rule-level fault should have suppressed the answer")
	case <-time.After(300 * time.Millisecond):
	}

	// A rule that doesn't match the faulty rule is unaffected.
	sendCCR(t, conn, "ok-1", 1)
	waitCCA(t, received)
}

func TestDiameterUnmatchedRequestsUseTheConfiguredDefaultResultCode(t *testing.T) {
	e := New()
	def := newDef("unmatched", &mock.DiameterConfig{
		DefaultResultCode: 5012,
		Rules:             []mock.DiameterRule{{CCRequestType: 1, ResultCode: 2001}},
	})
	addr := registerOnFreePort(t, e, def)
	received := make(chan *diam.Message, 2)
	conn := dialClient(t, addr, func(c diam.Conn, m *diam.Message) { received <- m })

	sendCCR(t, conn, "s1", 1) // matches the rule
	sendCCR(t, conn, "s1", 2) // matches nothing -> the configured default
	got := []uint32{resultCodeOf(t, waitCCA(t, received)), resultCodeOf(t, waitCCA(t, received))}
	if got[0] != 2001 || got[1] != 5012 {
		t.Fatalf("answers %v, want [2001 5012]", got)
	}
}

func TestDiameterRuleWithNoResultCodeStillMeansSuccessNotTheDefault(t *testing.T) {
	e := New()
	def := newDef("rule-default", &mock.DiameterConfig{DefaultResultCode: 5012, Rules: []mock.DiameterRule{{CCRequestType: 1}}})
	addr := registerOnFreePort(t, e, def)
	received := make(chan *diam.Message, 1)
	conn := dialClient(t, addr, func(c diam.Conn, m *diam.Message) { received <- m })
	sendCCR(t, conn, "s1", 1)
	if got := resultCodeOf(t, waitCCA(t, received)); got != 2001 {
		t.Fatalf("a matching rule with no resultCode must answer 2001, got %d", got)
	}
}
