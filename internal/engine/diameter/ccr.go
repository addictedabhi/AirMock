package diameterengine

import (
	"regexp"
	"strings"
	"time"

	"github.com/fiorix/go-diameter/v4/diam"
	"github.com/fiorix/go-diameter/v4/diam/avp"
	"github.com/fiorix/go-diameter/v4/diam/datatype"

	"github.com/addictedabhi/airmock/internal/mock"
)

const defaultResultCode = 2001 // DIAMETER_SUCCESS

// creditControlRequest is unmarshaled from an inbound CCR using
// diam.Message.Unmarshal's avp-tag reflection — the same mechanism
// go-diameter's own examples use — rather than looking up each AVP by
// name individually.
type creditControlRequest struct {
	SessionID       datatype.UTF8String       `avp:"Session-Id"`
	OriginHost      datatype.DiameterIdentity `avp:"Origin-Host"`
	OriginRealm     datatype.DiameterIdentity `avp:"Origin-Realm"`
	CCRequestType   datatype.Enumerated       `avp:"CC-Request-Type"`
	CCRequestNumber datatype.Unsigned32       `avp:"CC-Request-Number"`
}

// requestFacts is what rule matching and answer building need from a CCR,
// extracted once.
type requestFacts struct {
	ccRequestType   int
	sessionID       string
	subscriptionIDs []string  // Subscription-Id-Data of every Subscription-Id (443)
	mscc            []msccReq // one per Multiple-Services-Credit-Control (456)
}

type msccReq struct {
	ratingGroup       *uint32
	serviceIdentifier *uint32
}

func groupedOf(a *diam.AVP) []*diam.AVP {
	if g, ok := a.Data.(*diam.GroupedAVP); ok && g != nil {
		return g.AVP
	}
	return nil
}

// parseRequestFacts walks the CCR's top-level AVPs for the grouped AVPs
// the Unmarshal struct tags above can't express (they may repeat).
func parseRequestFacts(m *diam.Message, ccr *creditControlRequest) requestFacts {
	f := requestFacts{ccRequestType: int(ccr.CCRequestType), sessionID: string(ccr.SessionID)}
	for _, a := range m.AVP {
		switch a.Code {
		case avp.SubscriptionID:
			for _, in := range groupedOf(a) {
				if in.Code == avp.SubscriptionIDData {
					if v, ok := in.Data.(datatype.UTF8String); ok {
						f.subscriptionIDs = append(f.subscriptionIDs, string(v))
					}
				}
			}
		case avp.MultipleServicesCreditControl:
			var r msccReq
			for _, in := range groupedOf(a) {
				switch in.Code {
				case avp.RatingGroup:
					if v, ok := in.Data.(datatype.Unsigned32); ok {
						x := uint32(v)
						r.ratingGroup = &x
					}
				case avp.ServiceIdentifier:
					if v, ok := in.Data.(datatype.Unsigned32); ok {
						x := uint32(v)
						r.serviceIdentifier = &x
					}
				}
			}
			f.mscc = append(f.mscc, r)
		}
	}
	return f
}

// handleCCR answers every inbound Credit-Control-Request. The CER/CEA
// handshake and DWR/DWA watchdog that must happen before this is ever
// invoked are handled entirely by the sm.StateMachine this is registered
// against (see RegisterMock) — handlers registered there only run once a
// peer has already passed the handshake.
func (e *Engine) handleCCR(def *mock.Definition) diam.HandlerFunc {
	return func(c diam.Conn, m *diam.Message) {
		var ccr creditControlRequest
		if err := m.Unmarshal(&ccr); err != nil {
			return // malformed request — nothing sane to answer with
		}

		regID := regIDOf(c)
		e.sessReg.Touch(def.ID, regID, 1, 0)
		// Best-effort: only the CCR itself carries the peer's real
		// Origin-Host/Realm here (the CER/CEA handshake that established
		// this connection is handled entirely inside sm.StateMachine,
		// with no hook this engine can observe) — captured on the first
		// CCR and left alone afterward since a peer's identity doesn't
		// change mid-connection.
		e.sessReg.UpdateMeta(def.ID, regID, map[string]string{
			"originHost":  string(ccr.OriginHost),
			"originRealm": string(ccr.OriginRealm),
		})

		facts := parseRequestFacts(m, &ccr)
		rule := matchDiameterRule(def.Diameter, facts)
		resultCode := ruleResultCode(rule)
		if rule == nil && def.Diameter != nil && def.Diameter.DefaultResultCode != 0 {
			resultCode = def.Diameter.DefaultResultCode
		}

		// A rule's own fault settings replace the mock-level ones.
		fault := def.Fault
		if rule != nil && rule.Fault != nil {
			fault = rule.Fault
		}
		switch mock.RollFault(fault) {
		case "timeout", "error":
			e.recordHit(def, facts.sessionID, facts.ccRequestType, resultCode, false)
			return // simulate the peer never answering
		}
		if rule != nil && rule.DelayMs > 0 {
			time.Sleep(time.Duration(rule.DelayMs) * time.Millisecond)
		}
		if fault != nil && fault.LatencyJitterMs > 0 {
			time.Sleep(mock.RandomJitter(fault.LatencyJitterMs))
		}

		a := m.Answer(uint32(resultCode))
		a.NewAVP(avp.SessionID, avp.Mbit, 0, ccr.SessionID)
		a.NewAVP(avp.OriginHost, avp.Mbit, 0, datatype.DiameterIdentity(originHostOf(def.Diameter)))
		a.NewAVP(avp.OriginRealm, avp.Mbit, 0, datatype.DiameterIdentity(originRealmOf(def.Diameter)))
		a.NewAVP(avp.CCRequestType, avp.Mbit, 0, ccr.CCRequestType)
		a.NewAVP(avp.CCRequestNumber, avp.Mbit, 0, ccr.CCRequestNumber)
		for _, g := range buildMSCCAnswers(rule, facts) {
			a.AddAVP(g)
		}
		if _, err := a.WriteTo(c); err == nil {
			e.sessReg.Touch(def.ID, regID, 0, 1)
		}

		e.recordHit(def, facts.sessionID, facts.ccRequestType, resultCode, true)
	}
}

func ruleResultCode(r *mock.DiameterRule) int {
	if r == nil || r.ResultCode == 0 {
		return defaultResultCode
	}
	return r.ResultCode
}

// finalUnitActionCodes maps the rule's FinalUnitAction names to the
// Final-Unit-Action enumeration of RFC 4006 section 8.35.
var finalUnitActionCodes = map[string]int32{"terminate": 0, "redirect": 1, "restrict_access": 2}

// buildMSCCAnswers returns the Multiple-Services-Credit-Control AVPs for a
// CCA: one per MSCC in the request (echoing its Rating-Group and
// Service-Identifier), or a single bare one when the request had none. Empty
// when the matched rule asks for no grant, or for TERMINATION_REQUESTs
// (which never carry granted units).
func buildMSCCAnswers(r *mock.DiameterRule, f requestFacts) []*diam.AVP {
	if r == nil || !r.HasGrant() || f.ccRequestType == 3 {
		return nil
	}
	reqs := f.mscc
	if len(reqs) == 0 {
		reqs = []msccReq{{}}
	}
	out := make([]*diam.AVP, 0, len(reqs))
	for _, q := range reqs {
		var in []*diam.AVP
		if q.ratingGroup != nil {
			in = append(in, diam.NewAVP(avp.RatingGroup, avp.Mbit, 0, datatype.Unsigned32(*q.ratingGroup)))
		}
		if q.serviceIdentifier != nil {
			in = append(in, diam.NewAVP(avp.ServiceIdentifier, avp.Mbit, 0, datatype.Unsigned32(*q.serviceIdentifier)))
		}
		var gsu []*diam.AVP
		if r.GrantedTime != nil {
			gsu = append(gsu, diam.NewAVP(avp.CCTime, avp.Mbit, 0, datatype.Unsigned32(*r.GrantedTime)))
		}
		if r.GrantedTotalOctets != nil {
			gsu = append(gsu, diam.NewAVP(avp.CCTotalOctets, avp.Mbit, 0, datatype.Unsigned64(*r.GrantedTotalOctets)))
		}
		if r.GrantedServiceSpecificUnits != nil {
			gsu = append(gsu, diam.NewAVP(avp.CCServiceSpecificUnits, avp.Mbit, 0, datatype.Unsigned64(*r.GrantedServiceSpecificUnits)))
		}
		if len(gsu) > 0 {
			in = append(in, diam.NewAVP(avp.GrantedServiceUnit, avp.Mbit, 0, &diam.GroupedAVP{AVP: gsu}))
		}
		if r.ValidityTime != nil {
			in = append(in, diam.NewAVP(avp.ValidityTime, avp.Mbit, 0, datatype.Unsigned32(*r.ValidityTime)))
		}
		if code, ok := finalUnitActionCodes[r.FinalUnitAction]; ok {
			in = append(in, diam.NewAVP(avp.FinalUnitIndication, avp.Mbit, 0, &diam.GroupedAVP{AVP: []*diam.AVP{
				diam.NewAVP(avp.FinalUnitAction, avp.Mbit, 0, datatype.Enumerated(code)),
			}}))
		}
		out = append(out, diam.NewAVP(avp.MultipleServicesCreditControl, avp.Mbit, 0, &diam.GroupedAVP{AVP: in}))
	}
	return out
}

// matchDiameterRule is first-match-wins over cfg.Rules. A rule matches when
// every criterion it sets holds: CC-Request-Type, Session-Id, a
// Subscription-Id-Data value, and a Rating-Group present on one of the
// request's MSCCs. Returns nil when nothing matches (answer: plain 2001).
func matchDiameterRule(cfg *mock.DiameterConfig, f requestFacts) *mock.DiameterRule {
	if cfg == nil {
		return nil
	}
	for i := range cfg.Rules {
		r := &cfg.Rules[i]
		if r.CCRequestType != 0 && r.CCRequestType != f.ccRequestType {
			continue
		}
		if r.SessionIDMatch != "" && !diameterTextMatches(r.MatchType, r.SessionIDMatch, f.sessionID) {
			continue
		}
		if r.SubscriptionIDMatch != "" {
			found := false
			for _, id := range f.subscriptionIDs {
				if diameterTextMatches(r.MatchType, r.SubscriptionIDMatch, id) {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		if r.RatingGroup != 0 {
			found := false
			for _, q := range f.mscc {
				if q.ratingGroup != nil && *q.ratingGroup == r.RatingGroup {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		return r
	}
	return nil
}

func diameterTextMatches(matchType, pattern, value string) bool {
	switch matchType {
	case "exact":
		return value == pattern
	case "regex":
		matched, err := regexp.MatchString(pattern, value)
		return err == nil && matched
	default: // "contains"
		return strings.Contains(value, pattern)
	}
}
