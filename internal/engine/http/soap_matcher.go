package httpengine

import (
	"bytes"
	"net/http"
	"strings"
	"time"

	"github.com/antchfx/xmlquery"

	"github.com/addictedabhi/airmock/internal/mock"
)

const soapContentType = "text/xml; charset=utf-8"

// serveSoap dispatches a request against every SOAP mock sharing this
// endpoint's path, matching by SOAPAction header first (the standard way a
// SOAP client identifies the operation), falling back to the envelope
// body's operation element name when the header is absent.
func (e *Engine) serveSoap(w http.ResponseWriter, r *http.Request, ops []*mock.Definition) {
	bodyBytes, _, ok := readRequestBody(w, r)
	if !ok {
		return
	}

	reqCtx := mock.BuildRequestContext(r, bodyBytes, nil)

	def := matchSOAPOperation(ops, r.Header.Get("SOAPAction"), bodyBytes)
	if def == nil {
		writeSOAPFault(w, "Client", "no matching SOAP operation for this request")
		return
	}

	cw := newCapturingWriter(w)
	start := time.Now()
	defer e.logInboundHit(cw, r, def, bodyBytes, start)
	w = cw

	if e.runValidation(w, def, reqCtx) {
		return
	}

	if def.Mode == "async" && def.AsyncConfig != nil {
		e.serveAsync(w, reqCtx, def, soapContentType)
		return
	}

	if def.Scenario != nil && len(def.Scenario.Steps) > 0 {
		e.serveScenario(w, r, reqCtx, def, soapContentType)
		return
	}

	e.writeTemplatedResponseWithDefaultContentType(w, mock.SelectResponse(def, reqCtx), reqCtx, soapContentType, def.Fault, def.ID)
}

func matchSOAPOperation(ops []*mock.Definition, soapActionHeader string, bodyBytes []byte) *mock.Definition {
	action := strings.Trim(soapActionHeader, `"`)
	if action != "" {
		for _, d := range ops {
			if d.SOAPAction != "" && d.SOAPAction == action {
				return d
			}
		}
	}

	opName := detectOperationName(bodyBytes)
	if opName != "" {
		for _, d := range ops {
			if d.OperationName != "" && d.OperationName == opName {
				return d
			}
		}
	}

	// Exactly one SOAP mock registered on this path and neither signal
	// helped identify the operation — serve it rather than fault, since
	// there's nothing else it could be.
	if len(ops) == 1 {
		return ops[0]
	}
	return nil
}

// detectOperationName finds the SOAP Body's first child element's local
// name — the operation name in document/literal-style SOAP, which is the
// vast majority of real-world services.
func detectOperationName(bodyBytes []byte) string {
	if len(bodyBytes) == 0 {
		return ""
	}
	doc, err := xmlquery.Parse(bytes.NewReader(bodyBytes))
	if err != nil {
		return ""
	}
	body := xmlquery.FindOne(doc, "//*[local-name()='Body']")
	if body == nil {
		return ""
	}
	for child := body.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == xmlquery.ElementNode {
			return child.Data
		}
	}
	return ""
}

func writeSOAPFault(w http.ResponseWriter, faultCode, message string) {
	w.Header().Set("Content-Type", soapContentType)
	w.WriteHeader(http.StatusInternalServerError)
	w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Body>
    <soap:Fault>
      <faultcode>soap:` + faultCode + `</faultcode>
      <faultstring>` + message + `</faultstring>
    </soap:Fault>
  </soap:Body>
</soap:Envelope>`))
}
