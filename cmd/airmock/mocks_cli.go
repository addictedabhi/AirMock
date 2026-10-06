package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// newMocksCmd is AirMock's "mocks-as-code" surface: export/list/apply talk
// to a running instance's existing admin REST API (the same one the web UI
// uses) over plain HTTP, so no new server-side endpoint or storage access is
// needed here — this file is purely a scriptable client, which also means
// it works against a remote AirMock instance, not just one on localhost.
func newMocksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mocks",
		Short: "Manage mocks on a running AirMock instance as code (export/apply/list)",
	}
	cmd.PersistentFlags().String("url", defaultAirmockURL(), "AirMock admin URL (env AIRMOCK_URL also works)")
	addAuthFlags(cmd)
	cmd.PersistentPreRunE = loginIfNeeded
	cmd.AddCommand(newMocksExportCmd())
	cmd.AddCommand(newMocksApplyCmd())
	cmd.AddCommand(newMocksListCmd())
	return cmd
}

func defaultAirmockURL() string {
	if v := os.Getenv("AIRMOCK_URL"); v != "" {
		return v
	}
	return "http://localhost:8080"
}

// cliClient is shared by every CLI request so the session cookie obtained by
// loginIfNeeded is sent along with each call.
var cliClient = newCLIClient()

func newCLIClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Timeout: 30 * time.Second, Jar: jar}
}

func mocksHTTPClient() *http.Client { return cliClient }

// addAuthFlags registers --password on a command that talks to the admin API.
func addAuthFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().String("password", os.Getenv("AIRMOCK_ADMIN_PASSWORD"), "admin password/PIN for an instance with login enabled (env AIRMOCK_ADMIN_PASSWORD also works)")
}

// loginIfNeeded logs in to the target instance when --password is given, so
// the CLI works against an instance that has admin login enabled. With no
// password it does nothing (login-disabled instances need no credentials).
func loginIfNeeded(cmd *cobra.Command, args []string) error {
	// Arguments and flags parsed fine by now, so any error from here on is a
	// runtime failure (unreachable server, rejected mock), not a usage
	// mistake: don't bury it under the command's help text.
	cmd.SilenceUsage = true
	pw, _ := cmd.Flags().GetString("password")
	if pw == "" {
		return nil
	}
	url, _ := cmd.Flags().GetString("url")
	return loginTo(url, pw)
}

func loginTo(baseURL, password string) error {
	b, _ := json.Marshal(map[string]string{"password": password})
	resp, err := mocksHTTPClient().Post(baseURL+"/api/auth/login", "application/json", bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("login %s: %w", baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("login failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func newMocksExportCmd() *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Download every mock as a mocks-as-code file",
		RunE: func(cmd *cobra.Command, args []string) error {
			url, _ := cmd.Flags().GetString("url")
			body, err := httpGet(url + "/api/mocks/export")
			if err != nil {
				return err
			}
			if out == "" {
				fmt.Print(string(body))
				return nil
			}
			if err := os.WriteFile(out, body, 0o644); err != nil {
				return fmt.Errorf("write %s: %w", out, err)
			}
			fmt.Printf("Wrote %s\n", out)
			return nil
		},
	}
	cmd.Flags().StringVarP(&out, "out", "o", "", "file to write (default: stdout)")
	return cmd
}

func newMocksListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List mocks configured on the instance",
		RunE: func(cmd *cobra.Command, args []string) error {
			url, _ := cmd.Flags().GetString("url")
			body, err := httpGet(url + "/api/mocks")
			if err != nil {
				return err
			}
			var defs []map[string]any
			if err := json.Unmarshal(body, &defs); err != nil {
				return fmt.Errorf("parse response: %w", err)
			}
			sort.Slice(defs, func(i, j int) bool {
				return fmt.Sprint(defs[i]["name"]) < fmt.Sprint(defs[j]["name"])
			})
			printMocksTable(defs)
			return nil
		},
	}
}

func printMocksTable(defs []map[string]any) {
	if len(defs) == 0 {
		fmt.Println("No mocks configured.")
		return
	}
	fmt.Printf("%-30s %-9s %-7s %-30s %s\n", "NAME", "PROTOCOL", "METHOD", "PATH/PORT", "ENABLED")
	for _, d := range defs {
		fmt.Printf("%-30s %-9s %-7s %-30s %v\n",
			truncate(fmt.Sprint(d["name"]), 30), fmt.Sprint(d["protocolType"]), fmt.Sprint(d["method"]),
			truncate(mockLocator(d), 30), d["enabled"])
	}
}

// mockLocator shows the path for HTTP-family mocks or the dedicated port for
// listener-based ones (TCP/SMTP/MQTT/FTP), whichever this mock actually has.
func mockLocator(d map[string]any) string {
	if p, ok := d["pathPattern"].(string); ok && p != "" {
		return p
	}
	for _, key := range []string{"tcp", "smtp", "mqtt", "ftp", "kafka", "smpp", "diameter", "jms"} {
		if cfg, ok := d[key].(map[string]any); ok {
			return fmt.Sprintf(":%v", cfg["port"])
		}
	}
	return "—"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func newMocksApplyCmd() *cobra.Command {
	var file, projectID string
	var dryRun, atomic bool
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Create-or-update mocks from a JSON or YAML mocks-as-code file",
		Long: `Applies every mock definition in the file: a mock whose name already
matches one on the instance is updated in place, everything else is created —
so re-running "apply" with the same file is safe (idempotent by name), which
is what makes it usable from a CI pipeline or a pre-commit hook rather than
only by hand. Accepts either the exact shape "mocks export" produces
({"mocks": [...]})  or a bare top-level array, in JSON or YAML (by file
extension).

Each mock is reported as created, updated or unchanged (an identical
definition is not rewritten, so it adds no version history). --dry-run
validates everything against the server and reports what would happen
without saving. --atomic does that validation first and applies nothing
unless every mock passes.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true // a failed apply is not a usage error
			if file == "" {
				cmd.SilenceUsage = false
				return fmt.Errorf("--file is required")
			}
			url, _ := cmd.Flags().GetString("url")
			return applyMocksFileOpts(url, file, projectID, applyOptions{DryRun: dryRun, Atomic: atomic})
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "mocks-as-code file (.json, .yaml, or .yml)")
	cmd.Flags().StringVar(&projectID, "project-id", "", "group every applied mock under this project id")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "validate and report what would be created/updated, without saving anything")
	cmd.Flags().BoolVar(&atomic, "atomic", false, "validate every mock first and apply nothing unless all of them pass")
	return cmd
}

func applyMocksFile(url, file, projectID string) error {
	return applyMocksFileOpts(url, file, projectID, applyOptions{})
}

func applyMocksFileOpts(url, file, projectID string, opts applyOptions) error {
	raw, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read %s: %w", file, err)
	}
	defs, err := parseMocksFile(raw, file)
	if err != nil {
		return fmt.Errorf("parse %s: %w", file, err)
	}
	if len(defs) == 0 {
		fmt.Println("No mock definitions found in the file.")
		return nil
	}
	return applyResources("mock", url, "/api/mocks", "/api/mocks", defs, func(def map[string]any) {
		if projectID != "" {
			def["projectId"] = projectID
		}
	}, opts)
}

// normalizeResourceName mirrors the server's own case-insensitive/trimmed
// uniqueness check (e.g. internal/mock.Store.nameTaken) so "apply" matches
// an existing mock/scheduled event the same way the server itself would
// consider a duplicate.
func normalizeResourceName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// printWarnings shows the non-fatal advice the server returns with a save
// (a "warnings" array), so it isn't lost when apply only reports
// created/updated.
func printWarnings(resp map[string]any) {
	ws, _ := resp["warnings"].([]any)
	for _, w := range ws {
		fmt.Printf("          warning: %v\n", w)
	}
}

func existingIDsByName(listURL string) (map[string]string, error) {
	body, err := httpGet(listURL)
	if err != nil {
		return nil, err
	}
	var defs []map[string]any
	if err := json.Unmarshal(body, &defs); err != nil {
		return nil, fmt.Errorf("parse existing resources: %w", err)
	}
	out := make(map[string]string, len(defs))
	for _, d := range defs {
		id, _ := d["id"].(string)
		name, _ := d["name"].(string)
		if id != "" && name != "" {
			out[normalizeResourceName(name)] = id
		}
	}
	return out, nil
}

// applyOptions tunes an apply run. DryRun validates every definition against
// the server (shape, templates, name/endpoint conflicts) and reports what
// would happen without saving anything. Atomic does that validation pass first
// and applies nothing unless every definition passes. Both rely on the
// server's ?dryRun=true support, so they are offered for mocks only.
type applyOptions struct {
	DryRun bool
	Atomic bool
}

// applyResourceByName is the shared create-or-update-by-name engine behind
// every mocks-as-code resource: a def whose name (case-insensitive/trimmed)
// already matches one on the instance is updated in place via PUT
// {createEndpoint}/{id}, everything else is POSTed to createEndpoint fresh
// — the same idempotent-by-name behavior "mocks apply" always had, reused
// as-is for scheduled events instead of re-implemented. mutate lets a
// caller attach resource-specific fields (e.g. mocks' --project-id) to each
// def right before it's sent, after id/createdAt/updatedAt are stripped.
func applyResourceByName(label, url, listEndpoint, createEndpoint string, defs []map[string]any, mutate func(def map[string]any)) error {
	return applyResources(label, url, listEndpoint, createEndpoint, defs, mutate, applyOptions{})
}

type plannedApply struct {
	def  map[string]any
	name string
	id   string // existing resource id, "" for a create
}

func applyResources(label, url, listEndpoint, createEndpoint string, defs []map[string]any, mutate func(def map[string]any), opts applyOptions) error {
	existingByName, err := existingIDsByName(url + listEndpoint)
	if err != nil {
		return err
	}

	plan := make([]plannedApply, 0, len(defs))
	for _, def := range defs {
		delete(def, "id")
		delete(def, "createdAt")
		delete(def, "updatedAt")
		if mutate != nil {
			mutate(def)
		}
		name := fmt.Sprint(def["name"])
		plan = append(plan, plannedApply{def: def, name: name, id: existingByName[normalizeResourceName(name)]})
	}

	if opts.DryRun || opts.Atomic {
		failures := dryRunPlan(label, url, createEndpoint, plan)
		if opts.DryRun {
			if failures > 0 {
				return fmt.Errorf("%d %s(s) would fail to apply", failures, label)
			}
			return nil
		}
		if failures > 0 {
			return fmt.Errorf("%d %s(s) failed validation; nothing was applied", failures, label)
		}
		fmt.Println()
	}

	var created, updated, unchanged, failed int
	for _, p := range plan {
		def, name := p.def, p.name
		normalized := normalizeResourceName(name)
		// An entry created earlier in this same run is an existing one for
		// any later entry that shares its (case/whitespace-insensitive) name.
		id := p.id
		if id == "" {
			id = existingByName[normalized]
		}
		if id != "" {
			def["id"] = id
			var updatedResp map[string]any
			hdr, err := httpJSONResp(http.MethodPut, url+createEndpoint+"/"+id, def, &updatedResp)
			if err != nil {
				fmt.Printf("  FAILED    update %-30s %v\n", name, err)
				failed++
				continue
			}
			if hdr.Get("X-AirMock-Unchanged") == "true" {
				fmt.Printf("  unchanged %s\n", name)
				unchanged++
			} else {
				fmt.Printf("  updated   %s\n", name)
				updated++
			}
			printWarnings(updatedResp)
			continue
		}
		// existingByName is fetched once, before this loop — without
		// recording each create's own ID back into it, two entries in the
		// SAME file sharing a (case/whitespace-insensitive) name both look
		// "not found" here and both get POSTed as separate creates. A mock's
		// name uniqueness is enforced server-side, so that case at least
		// fails loudly (a 409 reported as "FAILED create"); a scheduled
		// event's is not (internal/scheduledevent/store.go has no
		// nameTaken/ErrDuplicate* check at all), so it silently created two
		// duplicate rows, and every later "apply" run could only ever
		// update one of the two — the other stuck as a permanent orphan.
		var createdResp map[string]any
		if _, err := httpJSONResp(http.MethodPost, url+createEndpoint, def, &createdResp); err != nil {
			fmt.Printf("  FAILED    create %-30s %v\n", name, err)
			failed++
			continue
		}
		if newID, ok := createdResp["id"].(string); ok {
			existingByName[normalized] = newID
		}
		fmt.Printf("  created   %s\n", name)
		printWarnings(createdResp)
		created++
	}

	fmt.Printf("\n%d created, %d updated, %d unchanged, %d failed\n", created, updated, unchanged, failed)
	if failed > 0 {
		return fmt.Errorf("%d %s(s) failed to apply", failed, label)
	}
	return nil
}

// dryRunPlan asks the server to validate every planned create/update without
// saving, prints what would happen, and returns how many would fail.
func dryRunPlan(label, url, createEndpoint string, plan []plannedApply) int {
	seen := map[string]bool{}
	failures := 0
	var create, update, same int
	for _, p := range plan {
		key := normalizeResourceName(p.name)
		if seen[key] {
			fmt.Printf("  FAILED    %-30s duplicate name within the file\n", p.name)
			failures++
			continue
		}
		seen[key] = true

		method, target := http.MethodPost, url+createEndpoint+"?dryRun=true"
		if p.id != "" {
			method, target = http.MethodPut, url+createEndpoint+"/"+p.id+"?dryRun=true"
			p.def["id"] = p.id
		}
		var report map[string]any
		if _, err := httpJSONResp(method, target, p.def, &report); err != nil {
			fmt.Printf("  FAILED    %-30s %v\n", p.name, err)
			failures++
			continue
		}
		switch report["action"] {
		case "create":
			fmt.Printf("  would create    %s\n", p.name)
			create++
		case "unchanged":
			fmt.Printf("  unchanged       %s\n", p.name)
			same++
		default:
			fmt.Printf("  would update    %s\n", p.name)
			update++
		}
		printWarnings(report)
	}
	fmt.Printf("\ndry run: %d would be created, %d updated, %d unchanged, %d failed\n", create, update, same, failures)
	return failures
}

// parseMocksFile accepts either the {"mocks":[...]} wrapper "mocks export"
// produces or a bare top-level array, in JSON or YAML — sniffed by file
// extension since YAML has no reliable content-based signature the way JSON
// does (a document starting with "{" is unambiguous either way, but a plain
// list at the top level is valid in both formats).
func parseMocksFile(raw []byte, filename string) ([]map[string]any, error) {
	return parseResourceFile(raw, filename, "mocks")
}

// parseResourceFile is parseMocksFile generalized over the wrapper key, so
// the same JSON/YAML/bare-array-vs-wrapped-object parsing logic backs every
// mocks-as-code resource (mocks, scheduled events, ...) instead of each one
// re-implementing it.
func parseResourceFile(raw []byte, filename, wrapperKey string) ([]map[string]any, error) {
	var generic any
	isYAML := strings.HasSuffix(filename, ".yaml") || strings.HasSuffix(filename, ".yml")
	if isYAML {
		if err := yaml.Unmarshal(raw, &generic); err != nil {
			return nil, err
		}
	} else {
		if err := json.Unmarshal(raw, &generic); err != nil {
			return nil, err
		}
	}

	switch v := generic.(type) {
	case []any:
		return toMapSlice(v)
	case map[string]any:
		if field, ok := v[wrapperKey]; ok {
			if list, ok := field.([]any); ok {
				return toMapSlice(list)
			}
		}
		return nil, fmt.Errorf(`expected a top-level array or a {%q: [...]} object`, wrapperKey)
	default:
		return nil, fmt.Errorf("unrecognized file shape")
	}
}

func toMapSlice(list []any) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(list))
	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("entry %d is not an object", i)
		}
		out = append(out, m)
	}
	return out, nil
}

func httpGet(url string) ([]byte, error) {
	resp, err := mocksHTTPClient().Get(url)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: %s", resp.Status, string(body))
	}
	return body, nil
}

// httpJSON sends body as JSON via method and, if out != nil, decodes the
// response into it — used by apply's create/update calls, which only need
// the status/error, not the returned mock.
func httpJSON(method, url string, body any, out any) error {
	_, err := httpJSONResp(method, url, body, out)
	return err
}

// httpJSONResp is httpJSON that also returns the response headers (apply
// reads X-AirMock-Unchanged from them).
func httpJSONResp(method, url string, body any, out any) (http.Header, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(method, url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := mocksHTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return resp.Header, fmt.Errorf("%s: %s", resp.Status, string(respBody))
	}
	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return resp.Header, err
		}
	}
	return resp.Header, nil
}
