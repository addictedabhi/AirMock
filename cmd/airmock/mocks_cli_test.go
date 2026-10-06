package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeMocksServer is a minimal in-memory stand-in for the real /api/mocks
// admin endpoint, enough to exercise applyMocksFile's HTTP calls without a
// full server.New(...) instance — this is also the regression test for the
// applyResourceByName/parseResourceFile refactor extracted for
// "scheduled-events apply" to reuse: "mocks apply" must behave identically
// to before that extraction.
func fakeMocksServer(t *testing.T) (*httptest.Server, func() []map[string]any) {
	t.Helper()
	var mu sync.Mutex
	mocks := map[string]map[string]any{}
	nextID := 1

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/mocks":
			list := make([]map[string]any, 0, len(mocks))
			for _, m := range mocks {
				list = append(list, m)
			}
			json.NewEncoder(w).Encode(list)
		case r.Method == http.MethodPost && r.URL.Path == "/api/mocks":
			var m map[string]any
			json.NewDecoder(r.Body).Decode(&m)
			id := "mock-" + itoa(nextID)
			nextID++
			m["id"] = id
			mocks[id] = m
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(m)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/mocks/"):
			id := strings.TrimPrefix(r.URL.Path, "/api/mocks/")
			var m map[string]any
			json.NewDecoder(r.Body).Decode(&m)
			m["id"] = id
			mocks[id] = m
			json.NewEncoder(w).Encode(m)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	snapshot := func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		list := make([]map[string]any, 0, len(mocks))
		for _, m := range mocks {
			list = append(list, m)
		}
		return list
	}
	t.Cleanup(srv.Close)
	return srv, snapshot
}

func TestApplyMocksFileCreatesThenUpdatesByName(t *testing.T) {
	srv, snapshot := fakeMocksServer(t)

	file := filepath.Join(t.TempDir(), "mocks.json")
	body := `{"mocks": [{"name": "Get widget", "protocolType": "rest", "method": "GET", "pathPattern": "/widgets/{id}", "enabled": true}]}`
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if err := applyMocksFile(srv.URL, file, ""); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if got := snapshot(); len(got) != 1 {
		t.Fatalf("expected exactly 1 mock after the first apply, got %d: %+v", len(got), got)
	}

	updated := `{"mocks": [{"name": "  GET WIDGET", "protocolType": "rest", "method": "GET", "pathPattern": "/widgets/v2/{id}", "enabled": false}]}`
	if err := os.WriteFile(file, []byte(updated), 0o644); err != nil {
		t.Fatalf("write updated fixture: %v", err)
	}
	if err := applyMocksFile(srv.URL, file, ""); err != nil {
		t.Fatalf("second apply: %v", err)
	}

	got := snapshot()
	if len(got) != 1 {
		t.Fatalf("expected the second apply to update the same mock by case/whitespace-insensitive name match, not create a second one — got %d: %+v", len(got), got)
	}
	if got[0]["pathPattern"] != "/widgets/v2/{id}" || got[0]["enabled"] != false {
		t.Fatalf("expected the mock's fields to reflect the second file's values, got %+v", got[0])
	}
}

func TestApplyMocksFileSetsProjectID(t *testing.T) {
	srv, snapshot := fakeMocksServer(t)
	file := filepath.Join(t.TempDir(), "mocks.json")
	body := `[{"name": "Get widget", "protocolType": "rest", "method": "GET", "pathPattern": "/widgets"}]`
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := applyMocksFile(srv.URL, file, "proj-1"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got := snapshot()
	if len(got) != 1 || got[0]["projectId"] != "proj-1" {
		t.Fatalf("expected projectId proj-1 assigned, got %+v", got)
	}
}

func TestParseMocksFileRejectsWrongWrapperKey(t *testing.T) {
	if _, err := parseMocksFile([]byte(`{"scheduledEvents": [{"name":"x"}]}`), "mocks.json"); err == nil {
		t.Fatal("expected an error for a file using the scheduledEvents wrapper key instead of mocks")
	}
}

func TestLoginToSendsPasswordAndKeepsSessionCookie(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			var b struct{ Password string }
			_ = json.NewDecoder(r.Body).Decode(&b)
			if b.Password != "secret" {
				http.Error(w, "nope", http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "airmock_session", Value: "tok", Path: "/"})
		case "/api/mocks":
			if _, err := r.Cookie("airmock_session"); err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Write([]byte("[]"))
		}
	}))
	defer srv.Close()
	cliClient = newCLIClient()

	if err := loginTo(srv.URL, "wrong"); err == nil {
		t.Fatal("expected wrong password to fail")
	}
	if _, err := httpGet(srv.URL + "/api/mocks"); err == nil {
		t.Fatal("expected 401 before login")
	}
	if err := loginTo(srv.URL, "secret"); err != nil {
		t.Fatalf("login: %v", err)
	}
	if _, err := httpGet(srv.URL + "/api/mocks"); err != nil {
		t.Fatalf("expected authed request to succeed: %v", err)
	}
}

func TestApplyPrintsServerWarnings(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			w.Write([]byte("[]"))
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":"abc","warnings":["asyncConfig: the callback has no callbackBodyTemplate"]}`))
		}
	}))
	defer srv.Close()
	cliClient = newCLIClient()

	old := os.Stdout
	rd, wr, _ := os.Pipe()
	os.Stdout = wr
	err := applyResourceByName("mock", srv.URL, "/api/mocks", "/api/mocks", []map[string]any{{"name": "cb"}}, nil)
	wr.Close()
	os.Stdout = old
	out, _ := io.ReadAll(rd)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(string(out), "warning: asyncConfig: the callback has no callbackBodyTemplate") {
		t.Fatalf("expected the warning in apply's output, got:\n%s", out)
	}
}

// ---- apply: unchanged, dry-run, atomic ----

type recordedCall struct{ method, path, query string }

// applyServer fakes the admin API: GET /api/mocks lists one existing mock
// ("existing"); a request is rejected (400) when its mock is named "bad";
// "existing" is reported unchanged on PUT.
func applyServer(t *testing.T) (*httptest.Server, *[]recordedCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []recordedCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, recordedCall{r.Method, r.URL.Path, r.URL.RawQuery})
		mu.Unlock()
		if r.Method == http.MethodGet {
			w.Write([]byte(`[{"id":"id-existing","name":"existing"}]`))
			return
		}
		var def map[string]any
		_ = json.NewDecoder(r.Body).Decode(&def)
		if def["name"] == "bad" {
			http.Error(w, `{"error":"bad mock"}`, http.StatusBadRequest)
			return
		}
		dry := r.URL.Query().Get("dryRun") == "true"
		switch {
		case r.Method == http.MethodPut && def["name"] == "existing":
			w.Header().Set("X-AirMock-Unchanged", "true")
			if dry {
				w.Write([]byte(`{"dryRun":true,"action":"unchanged"}`))
				return
			}
			w.Write([]byte(`{"id":"id-existing"}`))
		case r.Method == http.MethodPut:
			if dry {
				w.Write([]byte(`{"dryRun":true,"action":"update"}`))
				return
			}
			w.Write([]byte(`{"id":"x"}`))
		default:
			if dry {
				w.Write([]byte(`{"dryRun":true,"action":"create"}`))
				return
			}
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":"new-id"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	rd, wr, _ := os.Pipe()
	os.Stdout = wr
	err := fn()
	wr.Close()
	os.Stdout = old
	out, _ := io.ReadAll(rd)
	return string(out), err
}

func writeMocksFile(t *testing.T, names ...string) string {
	t.Helper()
	var defs []string
	for _, n := range names {
		defs = append(defs, `{"name":"`+n+`","method":"GET","pathPattern":"/`+n+`"}`)
	}
	f := filepath.Join(t.TempDir(), "mocks.json")
	if err := os.WriteFile(f, []byte(`{"mocks":[`+strings.Join(defs, ",")+`]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestApplyReportsUnchangedInsteadOfUpdated(t *testing.T) {
	srv, _ := applyServer(t)
	cliClient = newCLIClient()
	out, err := captureStdout(t, func() error { return applyMocksFile(srv.URL, writeMocksFile(t, "existing", "fresh"), "") })
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(out, "unchanged existing") || !strings.Contains(out, "created   fresh") || !strings.Contains(out, "1 created, 0 updated, 1 unchanged, 0 failed") {
		t.Fatalf("unexpected output:\n%s", out)
	}
}

func TestApplyDryRunSavesNothing(t *testing.T) {
	srv, calls := applyServer(t)
	cliClient = newCLIClient()
	out, err := captureStdout(t, func() error {
		return applyMocksFileOpts(srv.URL, writeMocksFile(t, "existing", "fresh"), "", applyOptions{DryRun: true})
	})
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "would create    fresh") || !strings.Contains(out, "unchanged       existing") {
		t.Fatalf("unexpected output:\n%s", out)
	}
	for _, c := range *calls {
		if c.method != http.MethodGet && c.query != "dryRun=true" {
			t.Fatalf("a dry run made a real write: %+v", c)
		}
	}
}

func TestApplyAtomicAppliesNothingWhenOneMockIsInvalid(t *testing.T) {
	srv, calls := applyServer(t)
	cliClient = newCLIClient()
	out, err := captureStdout(t, func() error {
		return applyMocksFileOpts(srv.URL, writeMocksFile(t, "fresh", "bad"), "", applyOptions{Atomic: true})
	})
	if err == nil || !strings.Contains(err.Error(), "nothing was applied") {
		t.Fatalf("expected an atomic failure, got %v\n%s", err, out)
	}
	for _, c := range *calls {
		if c.method != http.MethodGet && c.query != "dryRun=true" {
			t.Fatalf("atomic apply wrote %+v even though one mock was invalid", c)
		}
	}
}

func TestApplyWithoutAtomicStillCreatesTheGoodMockAndReportsTheBadOne(t *testing.T) {
	srv, calls := applyServer(t)
	cliClient = newCLIClient()
	out, err := captureStdout(t, func() error { return applyMocksFile(srv.URL, writeMocksFile(t, "fresh", "bad"), "") })
	if err == nil || !strings.Contains(out, "created   fresh") || !strings.Contains(out, "FAILED    create bad") {
		t.Fatalf("expected the good mock created and the bad one reported, got err=%v\n%s", err, out)
	}
	posted := 0
	for _, c := range *calls {
		if c.method == http.MethodPost {
			posted++
		}
	}
	if posted != 2 {
		t.Fatalf("expected both mocks attempted, got %d POSTs", posted)
	}
}

func TestRuntimeErrorsSilenceTheUsageDump(t *testing.T) {
	cmd := newMocksCmd()
	apply, _, _ := cmd.Find([]string{"apply"})
	if err := loginIfNeeded(apply, nil); err != nil {
		t.Fatal(err)
	}
	if !apply.SilenceUsage {
		t.Fatal("a runtime failure must not print the usage text")
	}
}

func TestVersionFlagWorksLikeTheVersionSubcommand(t *testing.T) {
	root := newRootCmd()
	var out strings.Builder
	root.SetOut(&out)
	root.SetArgs([]string{"--version"})
	if err := root.Execute(); err != nil {
		t.Fatalf("--version failed: %v", err)
	}
	if !strings.HasPrefix(out.String(), "airmock ") || !strings.Contains(out.String(), version) {
		t.Fatalf("unexpected --version output: %q", out.String())
	}
}
