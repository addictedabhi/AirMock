package mock

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"time"

	"github.com/google/uuid"
)

var ErrNotFound = errors.New("mock: not found")

// ErrDuplicateName guards mock names being unique across every protocol
// family sharing this store (REST/SOAP/GraphQL/TCP all live in one table),
// so the mock list and any by-name lookup (e.g. the Log History mock
// filter) stay unambiguous.
var ErrDuplicateName = errors.New("mock: a mock with this name already exists")

// ErrDuplicateEndpoint guards two REST mocks from claiming the same
// method+path on the same effective gateway (see restEndpointTaken) — chi
// can only route one handler per method+pattern, so two such mocks would
// otherwise silently fight over the same route.
var ErrDuplicateEndpoint = errors.New("mock: another REST mock already uses this method and path on the same gateway")

type Store struct {
	db               *sql.DB
	settingsProvider SettingsProvider
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// SetSettingsProvider wires up the configurable version-history depth (see
// versions.go's snapshotVersion). Optional: a Store with none set falls back
// to maxVersionsPerMock's own hardcoded default, the same "nil-safe optional
// dependency" pattern already used throughout the engine package
// (SetHitLogger, SetCertProvider, ...).
func (s *Store) SetSettingsProvider(p SettingsProvider) {
	s.settingsProvider = p
}

// nameTaken reports whether another mock (any id other than excludeID)
// already uses name, compared case-insensitively and trimmed so "Widget"
// and " widget " are treated as the same name.
func (s *Store) nameTaken(name, excludeID string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM mocks WHERE lower(trim(name)) = lower(trim(?)) AND id != ?`,
		name, excludeID,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check duplicate mock name: %w", err)
	}
	return count > 0, nil
}

// restEndpointTaken reports whether another enabled-or-not REST or WS mock
// (any id other than excludeID) already claims method+pathPattern on the
// same *effective* gateway — resolved per mock via its project's
// GatewayPort, so two projects each with their own dedicated port may reuse
// the same path freely, while REST/WS mocks sharing the default gateway
// (ungrouped, or in a project with no custom port) must stay unique. Both
// protocols are checked together because they register into the exact same
// chi mux, one handler per (method, path) — a REST GET and a WS mock on the
// same path collide just as much as two REST mocks would. SOAP/GraphQL
// mocks are deliberately excluded — multiple operations legitimately
// sharing one POST endpoint, disambiguated inside the engine by
// SOAPAction/operation name, is the intended design there, not a collision.
func (s *Store) restEndpointTaken(method, pathPattern, projectID, excludeID string) (bool, error) {
	if method == "" || pathPattern == "" {
		return false, nil
	}
	port, err := s.GatewayPortForProject(projectID)
	if err != nil {
		return false, err
	}

	// Collect every candidate project_id and close these rows *before*
	// resolving each one's port below — GatewayPortForProject runs its own
	// query, and the store's connection pool is capped at a single
	// connection (see storage.Open), so an open outer *sql.Rows plus a
	// nested query would otherwise deadlock waiting for a connection that
	// only the still-open outer query holds.
	rows, err := s.db.Query(
		`SELECT project_id FROM mocks WHERE protocol_type IN ('rest', 'ws') AND method = ? AND path_pattern = ? AND id != ?`,
		method, pathPattern, excludeID,
	)
	if err != nil {
		return false, fmt.Errorf("check duplicate endpoint: %w", err)
	}
	var otherProjectIDs []string
	for rows.Next() {
		var otherProjectID string
		if err := rows.Scan(&otherProjectID); err != nil {
			rows.Close()
			return false, fmt.Errorf("scan duplicate endpoint check: %w", err)
		}
		otherProjectIDs = append(otherProjectIDs, otherProjectID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	rows.Close()

	for _, otherProjectID := range otherProjectIDs {
		otherPort, err := s.GatewayPortForProject(otherProjectID)
		if err != nil {
			return false, err
		}
		if otherPort == port {
			return true, nil
		}
	}
	return false, nil
}

// optionalJSONColumns is every mock field stored as a nullable JSON blob
// column, marshaled/scanned together so adding one more (as future phases
// will) means touching this one spot rather than every query.
type optionalJSONColumns struct {
	asyncConfig             any
	validation              any
	validationErrorResponse any
	responseRules           any
	scenario                any
	weighted                any
	fault                   any
	proxy                   any
	tcp                     any
	smtp                    any
	ws                      any
	mqtt                    any
	ftp                     any
	kafka                   any
	smpp                    any
	diameter                any
	jms                     any
}

// optionalColumnSpec pairs one Definition field with where its marshaled
// JSON lands in optionalJSONColumns — table-driven so marshalOptionalColumns
// stays a flat loop (one cognitive-complexity point per branch, not per
// field) as more optional columns are added.
type optionalColumnSpec struct {
	label string
	value any
	dest  *any
}

func marshalOptionalColumns(d *Definition) (optionalJSONColumns, error) {
	var cols optionalJSONColumns
	specs := []optionalColumnSpec{
		{"async config", d.AsyncConfig, &cols.asyncConfig},
		{"validation error response", d.ValidationErrorResponse, &cols.validationErrorResponse},
		{"scenario", d.Scenario, &cols.scenario},
		{"weighted config", d.Weighted, &cols.weighted},
		{"fault", d.Fault, &cols.fault},
		{"proxy", d.Proxy, &cols.proxy},
		{"tcp config", d.TCP, &cols.tcp},
		{"smtp config", d.SMTP, &cols.smtp},
		{"ws config", d.WS, &cols.ws},
		{"mqtt config", d.MQTT, &cols.mqtt},
		{"ftp config", d.FTP, &cols.ftp},
		{"kafka config", d.Kafka, &cols.kafka},
		{"smpp config", d.SMPP, &cols.smpp},
		{"diameter config", d.Diameter, &cols.diameter},
		{"jms config", d.JMS, &cols.jms},
	}
	for _, spec := range specs {
		v, err := marshalIfPresent(spec.value)
		if err != nil {
			return cols, fmt.Errorf("marshal %s: %w", spec.label, err)
		}
		*spec.dest = v
	}

	// Validation/ResponseRules are slices, not optional pointers — only
	// marshal non-empty ones so an empty slice stores as NULL, not "[]".
	if len(d.Validation) > 0 {
		v, err := marshalIfPresent(d.Validation)
		if err != nil {
			return cols, fmt.Errorf("marshal validation rules: %w", err)
		}
		cols.validation = v
	}
	if len(d.ResponseRules) > 0 {
		v, err := marshalIfPresent(d.ResponseRules)
		if err != nil {
			return cols, fmt.Errorf("marshal response rules: %w", err)
		}
		cols.responseRules = v
	}
	return cols, nil
}

// marshalIfPresent marshals v to a JSON string, or returns (nil, nil) if v
// is a nil pointer (any of the optional *XConfig fields on Definition) —
// checked via reflection rather than a per-type switch so adding another
// optional pointer config doesn't require touching this function again.
func marshalIfPresent(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	if rv := reflect.ValueOf(v); rv.Kind() == reflect.Ptr && rv.IsNil() {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func (s *Store) Create(d *Definition) (*Definition, error) {
	if d.ID == "" {
		d.ID = uuid.NewString()
	}
	if d.ProtocolType == "" {
		d.ProtocolType = "rest"
	}
	if d.Mode == "" {
		d.Mode = "sync"
	}
	if d.ProtocolType == "ws" {
		d.Method = http.MethodGet // a WS handshake is always a GET; stored so restEndpointTaken can match on it
	}
	if err := s.checkDuplicateConstraints(d); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	d.CreatedAt, d.UpdatedAt = now, now

	respJSON, err := json.Marshal(d.Response)
	if err != nil {
		return nil, fmt.Errorf("marshal response: %w", err)
	}
	cols, err := marshalOptionalColumns(d)
	if err != nil {
		return nil, err
	}

	_, err = s.db.Exec(
		`INSERT INTO mocks (id, name, protocol_type, method, path_pattern, enabled, mode, response_json, async_config_json, validation_json, validation_error_response_json, response_rules_json, soap_action, operation_name, scenario_json, weighted_json, fault_json, proxy_json, tcp_json, smtp_json, ws_json, mqtt_json, ftp_json, kafka_json, smpp_json, diameter_json, jms_json, project_id, workspace_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.Name, d.ProtocolType, d.Method, d.PathPattern, boolToInt(d.Enabled), d.Mode, string(respJSON),
		cols.asyncConfig, cols.validation, cols.validationErrorResponse, cols.responseRules,
		d.SOAPAction, d.OperationName, cols.scenario, cols.weighted, cols.fault, cols.proxy, cols.tcp, cols.smtp, cols.ws, cols.mqtt, cols.ftp, cols.kafka, cols.smpp, cols.diameter, cols.jms, d.ProjectID, d.WorkspaceID,
		formatTime(d.CreatedAt), formatTime(d.UpdatedAt),
	)
	if err != nil {
		return nil, fmt.Errorf("insert mock: %w", err)
	}
	return d, nil
}

// checkDuplicateConstraints enforces the two uniqueness rules every
// Create/Update must satisfy: no other mock may share this one's name, and
// no other REST or WS mock may share its method+path on the same effective
// gateway — both register exactly one chi handler per (method, path), same
// constraint, just always "GET" for WS since a handshake is a GET request.
// Shared between Create and Update to keep both in sync and each under the
// cognitive-complexity limit.
func (s *Store) checkDuplicateConstraints(d *Definition) error {
	if d.Name != "" {
		taken, err := s.nameTaken(d.Name, d.ID)
		if err != nil {
			return err
		}
		if taken {
			return ErrDuplicateName
		}
	}
	if d.ProtocolType == "rest" || d.ProtocolType == "ws" {
		// d.Method is already normalized to "GET" for ws mocks by
		// Create/Update before this runs.
		taken, err := s.restEndpointTaken(d.Method, d.PathPattern, d.ProjectID, d.ID)
		if err != nil {
			return err
		}
		if taken {
			return ErrDuplicateEndpoint
		}
	}
	return nil
}

func (s *Store) Update(d *Definition) (*Definition, error) {
	if err := s.snapshotVersion(d.ID); err != nil {
		return nil, fmt.Errorf("snapshot version history: %w", err)
	}
	d.UpdatedAt = time.Now().UTC()
	if d.Mode == "" {
		d.Mode = "sync"
	}
	if d.ProtocolType == "ws" {
		d.Method = http.MethodGet
	}
	if err := s.checkDuplicateConstraints(d); err != nil {
		return nil, err
	}
	respJSON, err := json.Marshal(d.Response)
	if err != nil {
		return nil, fmt.Errorf("marshal response: %w", err)
	}
	cols, err := marshalOptionalColumns(d)
	if err != nil {
		return nil, err
	}

	res, err := s.db.Exec(
		`UPDATE mocks SET name=?, protocol_type=?, method=?, path_pattern=?, enabled=?, mode=?, response_json=?,
		   async_config_json=?, validation_json=?, validation_error_response_json=?, response_rules_json=?,
		   soap_action=?, operation_name=?, scenario_json=?, weighted_json=?, fault_json=?, proxy_json=?, tcp_json=?, smtp_json=?, ws_json=?, mqtt_json=?, ftp_json=?, kafka_json=?, smpp_json=?, diameter_json=?, jms_json=?, project_id=?, workspace_id=?, updated_at=?
		 WHERE id=?`,
		d.Name, d.ProtocolType, d.Method, d.PathPattern, boolToInt(d.Enabled), d.Mode, string(respJSON),
		cols.asyncConfig, cols.validation, cols.validationErrorResponse, cols.responseRules,
		d.SOAPAction, d.OperationName, cols.scenario, cols.weighted, cols.fault, cols.proxy, cols.tcp, cols.smtp, cols.ws, cols.mqtt, cols.ftp, cols.kafka, cols.smpp, cols.diameter, cols.jms, d.ProjectID, d.WorkspaceID,
		formatTime(d.UpdatedAt), d.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("update mock: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return d, nil
}

func (s *Store) Delete(id string) error {
	res, err := s.db.Exec(`DELETE FROM mocks WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete mock: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	// Best-effort: a deleted mock's version history has nothing left to
	// restore onto, so it's cleaned up alongside it rather than left
	// orphaned. Not fatal to the delete itself if this fails.
	s.db.Exec(`DELETE FROM mock_versions WHERE mock_id=?`, id)
	// Same reasoning for this mock's own counters/CSV attachment (see
	// dynamicvalues.go) — owner_id has no FK/CASCADE to ride along with the
	// row above, so without this a deleted mock's dynamic-value state (and,
	// for a CSV attachment, its full uploaded content) would sit orphaned
	// in the database forever, keyed by a UUID nothing can ever reach again.
	s.db.Exec(`DELETE FROM dynamic_counters WHERE owner_id=?`, id)
	s.db.Exec(`DELETE FROM dynamic_csv_sources WHERE owner_id=?`, id)
	return nil
}

const selectMockColumns = `id, name, protocol_type, method, path_pattern, enabled, mode, response_json,
	async_config_json, validation_json, validation_error_response_json, response_rules_json,
	soap_action, operation_name, scenario_json, weighted_json, fault_json, proxy_json, tcp_json, smtp_json, ws_json, mqtt_json, ftp_json, kafka_json, smpp_json, diameter_json, jms_json, project_id, workspace_id, created_at, updated_at`

func (s *Store) Get(id string) (*Definition, error) {
	row := s.db.QueryRow(`SELECT `+selectMockColumns+` FROM mocks WHERE id=?`, id)
	return scanDefinition(row)
}

func (s *Store) List() ([]*Definition, error) {
	rows, err := s.db.Query(`SELECT ` + selectMockColumns + ` FROM mocks ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list mocks: %w", err)
	}
	defer rows.Close()

	out := []*Definition{}
	for rows.Next() {
		d, err := scanDefinition(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ListByProject returns every mock belonging to projectID, in creation
// order — used by MockProjectsHandler.delete to find which mocks need
// unregistering from their live engine before DeleteProject removes their
// database rows.
func (s *Store) ListByProject(projectID string) ([]*Definition, error) {
	rows, err := s.db.Query(`SELECT `+selectMockColumns+` FROM mocks WHERE project_id=? ORDER BY created_at ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list mocks by project: %w", err)
	}
	defer rows.Close()

	out := []*Definition{}
	for rows.Next() {
		d, err := scanDefinition(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

// rawOptionalColumns bundles the mocks table's nullable JSON-blob columns
// as scanned (still strings, not yet unmarshaled) — passed as one value
// between scanDefinition and populateOptionalFields to keep both functions
// under the parameter-count lint limit as more optional columns are added.
type rawOptionalColumns struct {
	asyncConfig             sql.NullString
	validation              sql.NullString
	validationErrorResponse sql.NullString
	responseRules           sql.NullString
	scenario                sql.NullString
	weighted                sql.NullString
	fault                   sql.NullString
	proxy                   sql.NullString
	tcp                     sql.NullString
	smtp                    sql.NullString
	ws                      sql.NullString
	mqtt                    sql.NullString
	ftp                     sql.NullString
	kafka                   sql.NullString
	smpp                    sql.NullString
	diameter                sql.NullString
	jms                     sql.NullString
}

func scanDefinition(row scanner) (*Definition, error) {
	var d Definition
	var enabledInt int
	var respJSON, createdAt, updatedAt string
	var raw rawOptionalColumns

	err := row.Scan(&d.ID, &d.Name, &d.ProtocolType, &d.Method, &d.PathPattern, &enabledInt, &d.Mode, &respJSON,
		&raw.asyncConfig, &raw.validation, &raw.validationErrorResponse, &raw.responseRules,
		&d.SOAPAction, &d.OperationName, &raw.scenario, &raw.weighted, &raw.fault, &raw.proxy, &raw.tcp, &raw.smtp, &raw.ws, &raw.mqtt, &raw.ftp, &raw.kafka, &raw.smpp, &raw.diameter, &raw.jms, &d.ProjectID, &d.WorkspaceID, &createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan mock: %w", err)
	}
	d.Enabled = enabledInt != 0
	if err := json.Unmarshal([]byte(respJSON), &d.Response); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	if err := populateOptionalFields(&d, raw); err != nil {
		return nil, err
	}

	if d.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	if d.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, fmt.Errorf("parse updated_at: %w", err)
	}
	return &d, nil
}

// populateOptionalFields unmarshals the mocks table's nullable JSON-blob
// columns. Each step is its own closure (rather than one flat sequence of
// generic calls) purely so this stays a one-line loop — cognitive
// complexity is counted per branch, not per closure — as more optional
// columns are added.
func populateOptionalFields(d *Definition, raw rawOptionalColumns) error {
	steps := []func() error{
		func() error { return unmarshalIfPresent(raw.asyncConfig, &d.AsyncConfig, "async config") },
		func() error { return unmarshalSliceIfPresent(raw.validation, &d.Validation, "validation rules") },
		func() error {
			return unmarshalIfPresent(raw.validationErrorResponse, &d.ValidationErrorResponse, "validation error response")
		},
		func() error { return unmarshalSliceIfPresent(raw.responseRules, &d.ResponseRules, "response rules") },
		func() error { return unmarshalIfPresent(raw.scenario, &d.Scenario, "scenario") },
		func() error { return unmarshalIfPresent(raw.weighted, &d.Weighted, "weighted config") },
		func() error { return unmarshalIfPresent(raw.fault, &d.Fault, "fault") },
		func() error { return unmarshalIfPresent(raw.proxy, &d.Proxy, "proxy") },
		func() error { return unmarshalIfPresent(raw.tcp, &d.TCP, "tcp config") },
		func() error { return unmarshalIfPresent(raw.smtp, &d.SMTP, "smtp config") },
		func() error { return unmarshalIfPresent(raw.ws, &d.WS, "ws config") },
		func() error { return unmarshalIfPresent(raw.mqtt, &d.MQTT, "mqtt config") },
		func() error { return unmarshalIfPresent(raw.ftp, &d.FTP, "ftp config") },
		func() error { return unmarshalIfPresent(raw.kafka, &d.Kafka, "kafka config") },
		func() error { return unmarshalIfPresent(raw.smpp, &d.SMPP, "smpp config") },
		func() error { return unmarshalIfPresent(raw.diameter, &d.Diameter, "diameter config") },
		func() error { return unmarshalIfPresent(raw.jms, &d.JMS, "jms config") },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

// unmarshalIfPresent allocates *dest and unmarshals into it when col holds
// a non-empty value, leaving dest nil (the zero value for an optional
// pointer field) otherwise.
func unmarshalIfPresent[T any](col sql.NullString, dest **T, label string) error {
	if !col.Valid || col.String == "" {
		return nil
	}
	*dest = new(T)
	if err := json.Unmarshal([]byte(col.String), *dest); err != nil {
		return fmt.Errorf("unmarshal %s: %w", label, err)
	}
	return nil
}

// unmarshalSliceIfPresent is unmarshalIfPresent's counterpart for slice
// fields (Validation, ResponseRules), which don't need allocation first.
func unmarshalSliceIfPresent[T any](col sql.NullString, dest *[]T, label string) error {
	if !col.Valid || col.String == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(col.String), dest); err != nil {
		return fmt.Errorf("unmarshal %s: %w", label, err)
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// modernc.org/sqlite's driver doesn't auto-convert TEXT columns into
// time.Time on Scan, so timestamps are stored/read as RFC3339Nano strings
// explicitly rather than relying on driver-specific time handling.
func formatTime(t time.Time) string {
	return t.Format(time.RFC3339Nano)
}

func parseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
}

// normalizeForStorage applies the same defaults Create and Update apply, to a
// copy, so a definition can be compared with or checked against stored ones.
func normalizeForStorage(d *Definition) *Definition {
	c := *d
	if c.ProtocolType == "" {
		c.ProtocolType = "rest"
	}
	if c.Mode == "" {
		c.Mode = "sync"
	}
	if c.ProtocolType == "ws" {
		c.Method = http.MethodGet
	}
	return &c
}

// CheckDuplicates reports ErrDuplicateName / ErrDuplicateEndpoint exactly as
// Create or Update would, without writing anything (used for dry runs).
func (s *Store) CheckDuplicates(d *Definition) error {
	return s.checkDuplicateConstraints(normalizeForStorage(d))
}

// SameDefinition reports whether saving incoming over stored would change
// nothing, ignoring ids and timestamps. A false negative (two equal mocks
// reported different) is harmless; a false positive cannot happen because
// the comparison is of the full serialized definitions.
func SameDefinition(stored, incoming *Definition) bool {
	a, b := normalizeForStorage(stored), normalizeForStorage(incoming)
	a.CreatedAt, a.UpdatedAt = time.Time{}, time.Time{}
	b.CreatedAt, b.UpdatedAt = time.Time{}, time.Time{}
	b.ID = a.ID
	ja, err1 := json.Marshal(a)
	jb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(ja) == string(jb)
}
