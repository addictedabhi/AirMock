package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/apiclient/soapui"
	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/mock"
	"github.com/addictedabhi/airmock/internal/wsdl"
)

const contentTypeHeader = "Content-Type"

type WSDLHandler struct {
	store *mock.Store
}

func NewWSDLHandler(store *mock.Store) *WSDLHandler {
	return &WSDLHandler{store: store}
}

func (h *WSDLHandler) Routes(r chi.Router) {
	r.Post("/import", h.importWSDL)
	r.Post("/import-soapui-mocks", h.importSoapUIMocks)
}

type wsdlImportRequest struct {
	WSDLContent string `json:"wsdlContent"`
	PathPattern string `json:"pathPattern"`
	ProjectID   string `json:"projectId,omitempty"`
}

// importWSDL parses a pasted WSDL document and scaffolds one SOAP mock per
// operation on the given endpoint path, each with a stub response envelope
// — the SOAP counterpart of a one-click mock setup.
func (h *WSDLHandler) importWSDL(w http.ResponseWriter, r *http.Request) {
	var req wsdlImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.PathPattern == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("pathPattern is required"))
		return
	}

	ops, err := wsdl.Parse([]byte(req.WSDLContent))
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("parse WSDL: %w", err))
		return
	}
	if len(ops) == 0 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("no operations found in the WSDL's portType"))
		return
	}

	created := make([]*mock.Definition, 0, len(ops))
	for _, op := range ops {
		// op.BindingName is only set when this operation has more than one
		// distinct SOAPAction across bindings (see wsdl.Parse) — the mock
		// store requires unique names, so those need disambiguating; the
		// common single-binding case leaves the name exactly as before.
		name := op.Name
		if op.BindingName != "" {
			name = op.Name + " (" + op.BindingName + ")"
		}
		def := &mock.Definition{
			Name:          name,
			ProtocolType:  "soap",
			Method:        http.MethodPost,
			PathPattern:   req.PathPattern,
			Enabled:       true,
			ProjectID:     req.ProjectID,
			SOAPAction:    op.SOAPAction,
			OperationName: op.Name,
			Response: mock.ResponseTemplate{
				StatusCode:   200,
				Headers:      map[string]string{contentTypeHeader: "text/xml; charset=utf-8"},
				BodyTemplate: stubSOAPEnvelope(op.Name),
			},
		}
		saved, err := h.store.Create(def)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if err := engine.Dispatch(saved); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		created = append(created, saved)
	}

	writeJSON(w, http.StatusCreated, created)
}

type soapUIMockImportRequest struct {
	ProjectXML string `json:"projectXml"`
	ProjectID  string `json:"projectId,omitempty"`
}

// importSoapUIMocks parses a pasted SoapUI project export (*.xml) and
// scaffolds one SOAP mock per MockService operation, each carrying that
// operation's own already-authored response body — the SoapUI-mock
// counterpart of importWSDL, which only has a generic stub to work with
// since a WSDL alone doesn't carry any response content.
func (h *WSDLHandler) importSoapUIMocks(w http.ResponseWriter, r *http.Request) {
	var req soapUIMockImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	scaffolds, err := soapui.ImportMockServices([]byte(req.ProjectXML))
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("parse soapui project: %w", err))
		return
	}
	if len(scaffolds) == 0 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("no mock service operations with a response found in this project"))
		return
	}

	created := make([]*mock.Definition, 0, len(scaffolds))
	for _, s := range scaffolds {
		saved, err := h.createSoapUIMock(s, req.ProjectID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		created = append(created, saved)
	}

	writeJSON(w, http.StatusCreated, created)
}

// createSoapUIMock creates and dispatches the mock for one scaffold under a
// disambiguating name suffix if needed (see uniqueSuffix, already used for
// the same reason importing a backup's mocks) — two different mock
// services commonly scaffold same-named operations (e.g. both implement a
// shared "Ping"), and the mock store requires every name to be unique.
func (h *WSDLHandler) createSoapUIMock(s soapui.MockServiceScaffold, projectID string) (*mock.Definition, error) {
	baseName := s.OperationName
	if s.Protocol == "rest" {
		baseName = s.ServiceName + " " + s.Method + " " + s.PathPattern
	}
	var saved *mock.Definition
	if _, err := uniqueSuffix(baseName, func(name string) (bool, error) {
		def := soapUIMockDefinition(s, name, projectID)
		c, createErr := h.store.Create(def)
		if errors.Is(createErr, mock.ErrDuplicateName) {
			return true, createErr
		}
		if createErr == nil {
			saved = c
		}
		return false, createErr
	}); err != nil {
		return nil, err
	}
	if err := engine.Dispatch(saved); err != nil {
		return nil, err
	}
	return saved, nil
}

// soapUIMockDefinition builds the mock.Definition for one scaffold,
// branching on Protocol since a SOAP mockService operation and a REST
// restMockService action dispatch completely differently — the former by
// OperationName on a shared POST endpoint, the latter by its own
// Method+PathPattern, same as any other REST mock.
func soapUIMockDefinition(s soapui.MockServiceScaffold, name, projectID string) *mock.Definition {
	if s.Protocol == "rest" {
		return &mock.Definition{
			Name:         name,
			ProtocolType: "rest",
			Method:       s.Method,
			PathPattern:  s.PathPattern,
			Enabled:      true,
			ProjectID:    projectID,
			Response: mock.ResponseTemplate{
				StatusCode:   s.StatusCode,
				Headers:      map[string]string{contentTypeHeader: "application/json"},
				BodyTemplate: s.ResponseBody,
			},
		}
	}
	return &mock.Definition{
		Name:          name,
		ProtocolType:  "soap",
		Method:        http.MethodPost,
		PathPattern:   s.PathPattern,
		Enabled:       true,
		ProjectID:     projectID,
		OperationName: s.OperationName,
		SOAPAction:    s.SOAPAction,
		Response: mock.ResponseTemplate{
			StatusCode:   s.StatusCode,
			Headers:      map[string]string{contentTypeHeader: "text/xml; charset=utf-8"},
			BodyTemplate: s.ResponseBody,
		},
	}
}

func stubSOAPEnvelope(operationName string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Body>
    <%sResponse xmlns="urn:airmock:stub">
      <result>stub response for %s</result>
    </%sResponse>
  </soap:Body>
</soap:Envelope>`, operationName, operationName, operationName)
}
