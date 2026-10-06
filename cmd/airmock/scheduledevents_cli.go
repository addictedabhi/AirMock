package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"
)

// newScheduledEventsCmd extends mocks-as-code to scheduled events: the same
// export/apply/list shape as "mocks", built on the same generic
// parseResourceFile/applyResourceByName helpers, talking to the
// /api/scheduled-events admin endpoint instead of /api/mocks. Scheduled
// events have no project grouping (unlike mocks), so there's no
// --project-id flag here.
func newScheduledEventsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "scheduled-events",
		Aliases: []string{"scheduledevents"},
		Short:   "Manage scheduled events on a running AirMock instance as code (export/apply/list)",
	}
	cmd.PersistentFlags().String("url", defaultAirmockURL(), "AirMock admin URL (env AIRMOCK_URL also works)")
	addAuthFlags(cmd)
	cmd.PersistentPreRunE = loginIfNeeded
	cmd.AddCommand(newScheduledEventsExportCmd())
	cmd.AddCommand(newScheduledEventsApplyCmd())
	cmd.AddCommand(newScheduledEventsListCmd())
	return cmd
}

func newScheduledEventsExportCmd() *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Download every scheduled event as a mocks-as-code file",
		RunE: func(cmd *cobra.Command, args []string) error {
			url, _ := cmd.Flags().GetString("url")
			body, err := httpGet(url + "/api/scheduled-events")
			if err != nil {
				return err
			}
			wrapped, err := wrapAsResource(body, "scheduledEvents")
			if err != nil {
				return err
			}
			if out == "" {
				fmt.Print(string(wrapped))
				return nil
			}
			if err := os.WriteFile(out, wrapped, 0o644); err != nil {
				return fmt.Errorf("write %s: %w", out, err)
			}
			fmt.Printf("Wrote %s\n", out)
			return nil
		},
	}
	cmd.Flags().StringVarP(&out, "out", "o", "", "file to write (default: stdout)")
	return cmd
}

// wrapAsResource re-wraps a plain "GET list" JSON array (what
// /api/scheduled-events returns) into the {wrapperKey: [...]} shape mocks
// export downloads directly from a dedicated /export endpoint — scheduled
// events have no such endpoint, so the CLI wraps the list response itself
// instead, keeping the exported file shape identical either way.
func wrapAsResource(listBody []byte, wrapperKey string) ([]byte, error) {
	var items []map[string]any
	if err := json.Unmarshal(listBody, &items); err != nil {
		return nil, fmt.Errorf("parse list response: %w", err)
	}
	return json.MarshalIndent(map[string]any{wrapperKey: items}, "", "  ")
}

func newScheduledEventsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List scheduled events configured on the instance",
		RunE: func(cmd *cobra.Command, args []string) error {
			url, _ := cmd.Flags().GetString("url")
			body, err := httpGet(url + "/api/scheduled-events")
			if err != nil {
				return err
			}
			var events []map[string]any
			if err := json.Unmarshal(body, &events); err != nil {
				return fmt.Errorf("parse response: %w", err)
			}
			sort.Slice(events, func(i, j int) bool {
				return fmt.Sprint(events[i]["name"]) < fmt.Sprint(events[j]["name"])
			})
			printScheduledEventsTable(events)
			return nil
		},
	}
}

func printScheduledEventsTable(events []map[string]any) {
	if len(events) == 0 {
		fmt.Println("No scheduled events configured.")
		return
	}
	fmt.Printf("%-30s %-8s %-7s %-40s %s\n", "NAME", "INTERVAL", "METHOD", "TARGET URL", "ENABLED")
	for _, e := range events {
		fmt.Printf("%-30s %-8v %-7s %-40s %v\n",
			truncate(fmt.Sprint(e["name"]), 30), e["intervalSecs"], fmt.Sprint(e["method"]),
			truncate(fmt.Sprint(e["targetUrl"]), 40), e["enabled"])
	}
}

func newScheduledEventsApplyCmd() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Create-or-update scheduled events from a JSON or YAML mocks-as-code file",
		Long: `Applies every scheduled event definition in the file: one whose name already
matches one on the instance is updated in place, everything else is created —
so re-running "apply" with the same file is safe (idempotent by name), same
as "mocks apply". Accepts either {"scheduledEvents": [...]} (what
"scheduled-events export" produces) or a bare top-level array, in JSON or
YAML (by file extension).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if file == "" {
				return fmt.Errorf("--file is required")
			}
			url, _ := cmd.Flags().GetString("url")
			return applyScheduledEventsFile(url, file)
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "mocks-as-code file (.json, .yaml, or .yml)")
	return cmd
}

func applyScheduledEventsFile(url, file string) error {
	raw, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read %s: %w", file, err)
	}
	defs, err := parseResourceFile(raw, file, "scheduledEvents")
	if err != nil {
		return fmt.Errorf("parse %s: %w", file, err)
	}
	if len(defs) == 0 {
		fmt.Println("No scheduled event definitions found in the file.")
		return nil
	}
	return applyResourceByName("scheduled event", url, "/api/scheduled-events", "/api/scheduled-events", defs, nil)
}
