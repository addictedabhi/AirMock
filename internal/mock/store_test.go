package mock

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/addictedabhi/airmock/internal/storage"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db)
}

func TestCreateGetListUpdateDeleteRoundTrip(t *testing.T) {
	s := newTestStore(t)

	created, err := s.Create(&Definition{
		Name:        "greeting",
		Method:      "GET",
		PathPattern: "/hello/{name}",
		Enabled:     true,
		Response:    ResponseTemplate{StatusCode: 200, BodyTemplate: "{}"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.CreatedAt.IsZero() {
		t.Fatal("expected CreatedAt to be set")
	}

	got, err := s.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "greeting" || got.CreatedAt.IsZero() {
		t.Fatalf("unexpected Get result: %+v", got)
	}

	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 mock in List, got %d", len(list))
	}

	got.Enabled = false
	updated, err := s.Update(got)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Enabled {
		t.Fatal("expected Enabled=false after update")
	}

	if err := s.Delete(created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(created.ID); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestWorkspaceIDRoundTripsThroughCreateUpdateGet(t *testing.T) {
	s := newTestStore(t)

	created, err := s.Create(&Definition{
		Name:        "greeting",
		Method:      "GET",
		PathPattern: "/hello",
		Enabled:     true,
		Response:    ResponseTemplate{StatusCode: 200, BodyTemplate: "{}"},
		WorkspaceID: "ws-1",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.WorkspaceID != "ws-1" {
		t.Fatalf("expected WorkspaceID to round-trip through Create, got %q", created.WorkspaceID)
	}

	got, err := s.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.WorkspaceID != "ws-1" {
		t.Fatalf("expected WorkspaceID to round-trip through Get, got %q", got.WorkspaceID)
	}

	got.WorkspaceID = "ws-2"
	updated, err := s.Update(got)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.WorkspaceID != "ws-2" {
		t.Fatalf("expected WorkspaceID to round-trip through Update, got %q", updated.WorkspaceID)
	}
	reGot, err := s.Get(created.ID)
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if reGot.WorkspaceID != "ws-2" {
		t.Fatalf("expected persisted WorkspaceID after update to be ws-2, got %q", reGot.WorkspaceID)
	}
}

func TestFTPConfigRoundTripsThroughStore(t *testing.T) {
	s := newTestStore(t)

	created, err := s.Create(&Definition{
		Name:         "ftp-echo",
		ProtocolType: "ftp",
		Enabled:      true,
		FTP: &FTPConfig{
			Port:     2121,
			Username: "admin",
			Password: "secret",
			Files:    []FTPFile{{Name: "hello.txt", Content: "hello world"}},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.FTP == nil || got.FTP.Port != 2121 || got.FTP.Username != "admin" {
		t.Fatalf("expected FTP config to round-trip through storage, got %+v", got.FTP)
	}
	if len(got.FTP.Files) != 1 || got.FTP.Files[0].Name != "hello.txt" || got.FTP.Files[0].Content != "hello world" {
		t.Fatalf("expected the FTP file to round-trip, got %+v", got.FTP.Files)
	}
}

func TestMQTTConfigRoundTripsThroughStore(t *testing.T) {
	s := newTestStore(t)

	created, err := s.Create(&Definition{
		Name:         "mqtt-echo",
		ProtocolType: "mqtt",
		Enabled:      true,
		MQTT: &MQTTConfig{
			Port: 1883,
			Rules: []MQTTRule{
				{TopicPattern: "sensors/+/read", ReplyTopic: "sensors/reply", ReplyPayload: "pong"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.MQTT == nil || got.MQTT.Port != 1883 {
		t.Fatalf("expected MQTT config to round-trip through storage, got %+v", got.MQTT)
	}
	if len(got.MQTT.Rules) != 1 || got.MQTT.Rules[0].TopicPattern != "sensors/+/read" {
		t.Fatalf("expected the MQTT rule to round-trip, got %+v", got.MQTT.Rules)
	}
}

func TestKafkaConfigRoundTripsThroughStore(t *testing.T) {
	s := newTestStore(t)

	created, err := s.Create(&Definition{
		Name:         "kafka-echo",
		ProtocolType: "kafka",
		Enabled:      true,
		Kafka: &KafkaConfig{
			Port: 9092,
			Rules: []KafkaRule{
				{TopicPattern: "requests", ReplyTopic: "replies", ReplyPayload: "pong"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Kafka == nil || got.Kafka.Port != 9092 {
		t.Fatalf("expected Kafka config to round-trip through storage, got %+v", got.Kafka)
	}
	if len(got.Kafka.Rules) != 1 || got.Kafka.Rules[0].TopicPattern != "requests" {
		t.Fatalf("expected the Kafka rule to round-trip, got %+v", got.Kafka.Rules)
	}
}

func TestSMPPConfigRoundTripsThroughStore(t *testing.T) {
	s := newTestStore(t)

	created, err := s.Create(&Definition{
		Name:         "smpp-echo",
		ProtocolType: "smpp",
		Enabled:      true,
		SMPP: &SMPPConfig{
			Port:     2775,
			SystemID: "airmock",
			Rules: []SMPPRule{
				{DestAddrPattern: "2000", MessageMatch: "BALANCE", ReplyMessage: "Your balance is $42"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SMPP == nil || got.SMPP.Port != 2775 || got.SMPP.SystemID != "airmock" {
		t.Fatalf("expected SMPP config to round-trip through storage, got %+v", got.SMPP)
	}
	if len(got.SMPP.Rules) != 1 || got.SMPP.Rules[0].DestAddrPattern != "2000" {
		t.Fatalf("expected the SMPP rule to round-trip, got %+v", got.SMPP.Rules)
	}
}

func TestDiameterConfigRoundTripsThroughStore(t *testing.T) {
	s := newTestStore(t)

	created, err := s.Create(&Definition{
		Name:         "diameter-echo",
		ProtocolType: "diameter",
		Enabled:      true,
		Diameter: &DiameterConfig{
			Port:        3868,
			OriginHost:  "pcrf.airmock.test",
			OriginRealm: "airmock.test",
			Rules: []DiameterRule{
				{CCRequestType: 3, ResultCode: 5012},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Diameter == nil || got.Diameter.Port != 3868 || got.Diameter.OriginHost != "pcrf.airmock.test" {
		t.Fatalf("expected Diameter config to round-trip through storage, got %+v", got.Diameter)
	}
	if len(got.Diameter.Rules) != 1 || got.Diameter.Rules[0].ResultCode != 5012 {
		t.Fatalf("expected the Diameter rule to round-trip, got %+v", got.Diameter.Rules)
	}
}

func TestJMSConfigRoundTripsThroughStore(t *testing.T) {
	s := newTestStore(t)

	created, err := s.Create(&Definition{
		Name:         "jms-echo",
		ProtocolType: "jms",
		Enabled:      true,
		JMS: &JMSConfig{
			Port: 5672,
			Rules: []JMSRule{
				{AddressPattern: "orders", ReplyAddress: "receipts", ReplyPayload: "ok"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.JMS == nil || got.JMS.Port != 5672 {
		t.Fatalf("expected JMS config to round-trip through storage, got %+v", got.JMS)
	}
	if len(got.JMS.Rules) != 1 || got.JMS.Rules[0].AddressPattern != "orders" {
		t.Fatalf("expected the JMS rule to round-trip, got %+v", got.JMS.Rules)
	}
}

func TestWSConfigRoundTripsThroughStore(t *testing.T) {
	s := newTestStore(t)

	created, err := s.Create(&Definition{
		Name:         "ws-echo",
		ProtocolType: "ws",
		Enabled:      true,
		PathPattern:  "/ws/echo",
		WS: &WSConfig{
			OnConnectMessage: `{"type":"welcome"}`,
			Interactions:     []WSInteraction{{Match: "ping", MatchType: "exact", Response: "pong"}},
			DefaultResponse:  "unrecognized",
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Method != "GET" {
		t.Fatalf("expected the store to normalize a ws mock's method to GET, got %q", created.Method)
	}

	got, err := s.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.WS == nil {
		t.Fatal("expected WS config to round-trip through storage")
	}
	if got.WS.OnConnectMessage != `{"type":"welcome"}` || got.WS.DefaultResponse != "unrecognized" {
		t.Fatalf("unexpected WS config after round-trip: %+v", got.WS)
	}
	if len(got.WS.Interactions) != 1 || got.WS.Interactions[0].Match != "ping" {
		t.Fatalf("expected the WS interaction to round-trip, got %+v", got.WS.Interactions)
	}
}

func TestWSMockCollidesWithRestMockOnSamePathAndDefaultGateway(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.Create(&Definition{
		Name: "rest-a", ProtocolType: "rest", Method: "GET", PathPattern: "/shared", Enabled: true,
		Response: ResponseTemplate{StatusCode: 200, BodyTemplate: "{}"},
	}); err != nil {
		t.Fatalf("Create rest: %v", err)
	}

	_, err := s.Create(&Definition{
		Name: "ws-a", ProtocolType: "ws", PathPattern: "/shared", Enabled: true, WS: &WSConfig{},
	})
	if err != ErrDuplicateEndpoint {
		t.Fatalf("expected ErrDuplicateEndpoint for a ws mock colliding with a rest GET on the same path, got %v", err)
	}
}

func TestSMTPConfigRoundTripsThroughStore(t *testing.T) {
	s := newTestStore(t)

	created, err := s.Create(&Definition{
		Name:         "inbound-mail",
		ProtocolType: "smtp",
		Enabled:      true,
		SMTP: &SMTPConfig{
			Port:          2525,
			Hostname:      "mail.airmock.test",
			Banner:        "220 mail.airmock.test ESMTP",
			DefaultAccept: true,
			Rules: []SMTPRule{
				{MatchField: "subject", Match: "spam", MatchType: "contains", Accept: false, ResponseCode: 550, ResponseMessage: "spam rejected"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.SMTP == nil || created.SMTP.Port != 2525 {
		t.Fatalf("expected SMTP config to be set on the created definition, got %+v", created.SMTP)
	}

	got, err := s.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SMTP == nil {
		t.Fatal("expected SMTP config to round-trip through storage")
	}
	if got.SMTP.Port != 2525 || got.SMTP.Hostname != "mail.airmock.test" || !got.SMTP.DefaultAccept {
		t.Fatalf("unexpected SMTP config after round-trip: %+v", got.SMTP)
	}
	if len(got.SMTP.Rules) != 1 || got.SMTP.Rules[0].Match != "spam" {
		t.Fatalf("expected the SMTP rule to round-trip, got %+v", got.SMTP.Rules)
	}

	got.SMTP.Port = 2626
	updated, err := s.Update(got)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.SMTP.Port != 2626 {
		t.Fatalf("expected updated SMTP port to persist, got %d", updated.SMTP.Port)
	}
	reGot, err := s.Get(created.ID)
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if reGot.SMTP.Port != 2626 {
		t.Fatalf("expected the update to actually persist, got %d", reGot.SMTP.Port)
	}
}

func TestCreateRejectsDuplicateNameCaseInsensitively(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.Create(&Definition{Name: "Widget API", Method: "GET", PathPattern: "/a", Response: ResponseTemplate{StatusCode: 200}}); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	_, err := s.Create(&Definition{Name: " widget api ", Method: "GET", PathPattern: "/b", Response: ResponseTemplate{StatusCode: 200}})
	if err != ErrDuplicateName {
		t.Fatalf("expected ErrDuplicateName, got %v", err)
	}
}

func TestUpdateRejectsRenamingIntoAnExistingName(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.Create(&Definition{Name: "one", Method: "GET", PathPattern: "/a", Response: ResponseTemplate{StatusCode: 200}}); err != nil {
		t.Fatalf("create one: %v", err)
	}
	two, err := s.Create(&Definition{Name: "two", Method: "GET", PathPattern: "/b", Response: ResponseTemplate{StatusCode: 200}})
	if err != nil {
		t.Fatalf("create two: %v", err)
	}

	two.Name = "one"
	if _, err := s.Update(two); err != ErrDuplicateName {
		t.Fatalf("expected ErrDuplicateName, got %v", err)
	}

	// Updating a mock without changing its own name must still succeed.
	two.Name = "two"
	if _, err := s.Update(two); err != nil {
		t.Fatalf("expected no-op rename to succeed, got %v", err)
	}
}

func TestCreateProjectRejectsDuplicateNameCaseInsensitively(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.CreateProject(&Project{Name: "Billing", BasePath: "/api/billing"}); err != nil {
		t.Fatalf("first CreateProject: %v", err)
	}
	_, err := s.CreateProject(&Project{Name: " billing "})
	if err != ErrDuplicateProjectName {
		t.Fatalf("expected ErrDuplicateProjectName, got %v", err)
	}
}

func TestDeleteProjectCascadesToItsMocks(t *testing.T) {
	s := newTestStore(t)

	p, err := s.CreateProject(&Project{Name: "Billing", BasePath: "/api/billing"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	m, err := s.Create(&Definition{
		Name: "get invoice", Method: "GET", PathPattern: "/api/billing/invoice",
		ProjectID: p.ID, Response: ResponseTemplate{StatusCode: 200},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	other, err := s.Create(&Definition{
		Name: "unrelated", Method: "GET", PathPattern: "/api/other",
		Response: ResponseTemplate{StatusCode: 200},
	})
	if err != nil {
		t.Fatalf("Create (unrelated): %v", err)
	}

	if err := s.DeleteProject(p.ID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}

	if _, err := s.Get(m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected the project's mock to be deleted too, got %v", err)
	}
	if _, err := s.Get(other.ID); err != nil {
		t.Fatalf("expected an unrelated (ungrouped) mock to survive, got %v", err)
	}

	projects, err := s.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 0 {
		t.Fatalf("expected the project itself to be gone, got %+v", projects)
	}
}

func TestListByProject(t *testing.T) {
	s := newTestStore(t)

	p, err := s.CreateProject(&Project{Name: "Billing", BasePath: "/api/billing"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	m, err := s.Create(&Definition{
		Name: "get invoice", Method: "GET", PathPattern: "/api/billing/invoice",
		ProjectID: p.ID, Response: ResponseTemplate{StatusCode: 200},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.Create(&Definition{
		Name: "unrelated", Method: "GET", PathPattern: "/api/other",
		Response: ResponseTemplate{StatusCode: 200},
	}); err != nil {
		t.Fatalf("Create (unrelated): %v", err)
	}

	got, err := s.ListByProject(p.ID)
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(got) != 1 || got[0].ID != m.ID {
		t.Fatalf("expected exactly the one project mock, got %+v", got)
	}
}

func TestCreateRejectsDuplicateRestEndpointOnTheSameGateway(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.Create(&Definition{
		Name: "a", ProtocolType: "rest", Method: "GET", PathPattern: "/widgets",
		Response: ResponseTemplate{StatusCode: 200},
	}); err != nil {
		t.Fatalf("first Create: %v", err)
	}

	_, err := s.Create(&Definition{
		Name: "b", ProtocolType: "rest", Method: "GET", PathPattern: "/widgets",
		Response: ResponseTemplate{StatusCode: 200},
	})
	if err != ErrDuplicateEndpoint {
		t.Fatalf("expected ErrDuplicateEndpoint, got %v", err)
	}

	// A different method on the same path is not a collision.
	if _, err := s.Create(&Definition{
		Name: "c", ProtocolType: "rest", Method: "POST", PathPattern: "/widgets",
		Response: ResponseTemplate{StatusCode: 200},
	}); err != nil {
		t.Fatalf("expected a different method on the same path to be allowed, got %v", err)
	}
}

func TestDuplicateRestEndpointAllowedAcrossProjectsWithDifferentDedicatedPorts(t *testing.T) {
	s := newTestStore(t)

	p1, err := s.CreateProject(&Project{Name: "team-a", GatewayPort: 19001})
	if err != nil {
		t.Fatalf("CreateProject p1: %v", err)
	}
	p2, err := s.CreateProject(&Project{Name: "team-b", GatewayPort: 19002})
	if err != nil {
		t.Fatalf("CreateProject p2: %v", err)
	}

	if _, err := s.Create(&Definition{
		Name: "a", ProtocolType: "rest", Method: "GET", PathPattern: "/status",
		ProjectID: p1.ID, Response: ResponseTemplate{StatusCode: 200},
	}); err != nil {
		t.Fatalf("Create in p1: %v", err)
	}
	// Same method+path, but a different dedicated port — not a real collision.
	if _, err := s.Create(&Definition{
		Name: "b", ProtocolType: "rest", Method: "GET", PathPattern: "/status",
		ProjectID: p2.ID, Response: ResponseTemplate{StatusCode: 200},
	}); err != nil {
		t.Fatalf("expected the same path on a different dedicated port to be allowed, got %v", err)
	}
	// Same method+path+port (both in p1) IS a collision.
	_, err = s.Create(&Definition{
		Name: "c", ProtocolType: "rest", Method: "GET", PathPattern: "/status",
		ProjectID: p1.ID, Response: ResponseTemplate{StatusCode: 200},
	})
	if err != ErrDuplicateEndpoint {
		t.Fatalf("expected ErrDuplicateEndpoint for a second mock in the same project on the same path, got %v", err)
	}
}

func TestSoapMocksMaySharePathAcrossMultipleOperations(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.Create(&Definition{
		Name: "op1", ProtocolType: "soap", Method: "POST", PathPattern: "/soap/service",
		SOAPAction: "Op1", Response: ResponseTemplate{StatusCode: 200},
	}); err != nil {
		t.Fatalf("Create op1: %v", err)
	}
	if _, err := s.Create(&Definition{
		Name: "op2", ProtocolType: "soap", Method: "POST", PathPattern: "/soap/service",
		SOAPAction: "Op2", Response: ResponseTemplate{StatusCode: 200},
	}); err != nil {
		t.Fatalf("expected SOAP operations sharing one endpoint to be allowed, got %v", err)
	}
}
