package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/TheMarstonConnell/ted/browser"
	"github.com/spf13/cobra"
)

type browserCaller func(context.Context, browser.Request) (browser.Response, error)

func newBrowserCommand(call browserCaller) *cobra.Command {
	var project, thread string
	var timeout time.Duration
	var isolated bool
	root := &cobra.Command{Use: "browser", Short: "Control the project's persistent headless browser through bash"}
	root.PersistentFlags().StringVar(&project, "project", "", "Project directory (default TED_PROJECT_ROOT or current directory)")
	root.PersistentFlags().StringVar(&thread, "thread", "", "Thread ID (default TED_THREAD_ID or manual)")
	root.PersistentFlags().DurationVar(&timeout, "timeout", 30*time.Second, "Operation timeout")
	root.PersistentFlags().BoolVar(&isolated, "isolated", false, "Use a clean isolated session (choose before opening tabs)")
	dispatch := func(cmd *cobra.Command, action string, params map[string]any) error {
		if timeout <= 0 || timeout > 10*time.Minute {
			return fmt.Errorf("timeout must be greater than zero and at most 10m")
		}
		dir := project
		if dir == "" {
			dir = os.Getenv("TED_PROJECT_ROOT")
		}
		canonical, err := browser.ProjectRoot(dir)
		if err != nil {
			return err
		}
		id := thread
		if id == "" {
			id = os.Getenv("TED_THREAD_ID")
		}
		if id == "" {
			id = "manual"
		}
		if params == nil {
			params = map[string]any{}
		}
		if isolated {
			params["isolated"] = true
		}
		response, err := call(cmd.Context(), browser.Request{Project: canonical, Thread: id, Action: action, Params: params, Timeout: timeout})
		if err != nil {
			return err
		}
		if err = json.NewEncoder(cmd.OutOrStdout()).Encode(response); err != nil {
			return err
		}
		if !response.OK {
			if response.Error != nil {
				return response.Error
			}
			return fmt.Errorf("browser operation failed")
		}
		return nil
	}
	simple := func(parent *cobra.Command, use, short, action string) {
		parent.AddCommand(&cobra.Command{Use: use, Short: short, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error { return dispatch(cmd, action, nil) }})
	}
	simple(root, "status", "Report browser status without starting Chrome", "status")
	simple(root, "snapshot", "Get compact page content and interactive element references", "snapshot")
	simple(root, "tabs", "List thread-owned tabs", "tabs")
	simple(root, "console", "Get bounded browser console output", "console")
	simple(root, "errors", "Get bounded browser errors", "errors")
	simple(root, "close", "Close this thread's tabs, preserving the project profile", "session-close")
	root.AddCommand(&cobra.Command{Use: "open URL", Short: "Navigate to a URL", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return dispatch(cmd, "open", map[string]any{"url": args[0]})
	}})
	for _, action := range []string{"click", "fill", "select", "press", "scroll", "wait", "screenshot"} {
		action := action
		var selector, ref, label, role, name, value, key, urlPattern string
		var x, y int
		var fullPage bool
		cmd := &cobra.Command{Use: action, Short: "Browser " + action, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
			params := map[string]any{}
			for k, v := range map[string]string{"selector": selector, "ref": ref, "label": label, "role": role, "name": name, "value": value, "key": key, "url-pattern": urlPattern} {
				if cmd.Flags().Changed(k) {
					params[k] = v
				}
			}
			if cmd.Flags().Changed("x") {
				params["x"] = x
			}
			if cmd.Flags().Changed("y") {
				params["y"] = y
			}
			if fullPage {
				params["full-page"] = true
			}
			targets := 0
			for _, v := range []string{selector, ref, label, role} {
				if v != "" {
					targets++
				}
			}
			if targets > 1 {
				return fmt.Errorf("choose only one targeting method: --selector, --ref, --label, or --role")
			}
			if name != "" && role == "" {
				return fmt.Errorf("--name requires --role")
			}
			if (action == "click" || action == "fill" || action == "select") && targets == 0 {
				return fmt.Errorf("%s requires a target", action)
			}
			if (action == "fill" || action == "select") && !cmd.Flags().Changed("value") {
				return fmt.Errorf("%s requires --value (which may be empty)", action)
			}
			if action == "press" && key == "" {
				return fmt.Errorf("press requires --key")
			}
			if action == "wait" && targets == 0 && urlPattern == "" {
				return fmt.Errorf("wait requires a target or --url-pattern")
			}
			return dispatch(cmd, action, params)
		}}
		cmd.Flags().StringVar(&selector, "selector", "", "CSS selector")
		cmd.Flags().StringVar(&ref, "ref", "", "Element reference from the latest snapshot")
		cmd.Flags().StringVar(&label, "label", "", "Exact accessible label")
		cmd.Flags().StringVar(&role, "role", "", "Accessible role")
		cmd.Flags().StringVar(&name, "name", "", "Exact accessible name (with --role)")
		switch action {
		case "fill", "select":
			cmd.Flags().StringVar(&value, "value", "", "Control value")
		case "press":
			cmd.Flags().StringVar(&key, "key", "", "Key to press, e.g. Enter or Tab")
		case "scroll":
			cmd.Flags().IntVar(&x, "x", 0, "Horizontal scroll delta")
			cmd.Flags().IntVar(&y, "y", 0, "Vertical scroll delta")
		case "wait":
			cmd.Flags().StringVar(&urlPattern, "url-pattern", "", "URL glob to wait for")
		case "screenshot":
			cmd.Flags().BoolVar(&fullPage, "full-page", false, "Capture the full page")
		}
		root.AddCommand(cmd)
	}
	tab := &cobra.Command{Use: "tab", Short: "Manage thread-owned tabs"}
	tab.AddCommand(&cobra.Command{Use: "new [URL]", Short: "Create and select a tab", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		p := map[string]any{}
		if len(args) > 0 {
			p["url"] = args[0]
		}
		return dispatch(cmd, "tab-new", p)
	}})
	for _, action := range []string{"select", "close"} {
		action := action
		tab.AddCommand(&cobra.Command{Use: action + " ID", Short: action + " a thread-owned tab", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			return dispatch(cmd, "tab-"+action, map[string]any{"id": args[0]})
		}})
	}
	root.AddCommand(tab)
	record := &cobra.Command{Use: "record", Short: "Record silent viewport video (requires ffmpeg)"}
	simple(record, "start", "Start recording the current tab", "record-start")
	simple(record, "stop", "Stop recording and save a video artifact", "record-stop")
	root.AddCommand(record)
	var rawParams, paramFile string
	var browserScope bool
	cdp := &cobra.Command{Use: "cdp METHOD", Short: "Execute a raw CDP method (no high-level action guarantees)", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().Changed("params") && cmd.Flags().Changed("params-file") {
			return fmt.Errorf("use --params or --params-file, not both")
		}
		data := []byte(rawParams)
		if paramFile != "" {
			var reader io.Reader
			if paramFile == "-" {
				reader = cmd.InOrStdin()
			} else {
				f, err := os.Open(paramFile)
				if err != nil {
					return err
				}
				defer f.Close()
				reader = f
			}
			var err error
			data, err = io.ReadAll(io.LimitReader(reader, (8<<20)+1))
			if err != nil {
				return err
			}
			if len(data) > 8<<20 {
				return fmt.Errorf("CDP parameters exceed 8 MiB")
			}
		}
		var params map[string]any
		if err := json.Unmarshal(data, &params); err != nil {
			return fmt.Errorf("CDP parameters must be a JSON object: %w", err)
		}
		if params == nil {
			return fmt.Errorf("CDP parameters must be a JSON object")
		}
		return dispatch(cmd, "cdp", map[string]any{"method": args[0], "params": params, "browser": browserScope})
	}}
	cdp.Flags().StringVar(&rawParams, "params", "{}", "JSON parameters")
	cdp.Flags().StringVar(&paramFile, "params-file", "", "Read JSON parameters from a file, or - for stdin")
	cdp.Flags().BoolVar(&browserScope, "browser", false, "Execute against the shared browser rather than this tab")
	var limit int
	listen := &cobra.Command{Use: "listen EVENT", Short: "Collect bounded CDP events until the timeout or limit", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if limit < 1 || limit > 1000 {
			return fmt.Errorf("limit must be between 1 and 1000")
		}
		return dispatch(cmd, "cdp-listen", map[string]any{"event": args[0], "limit": limit})
	}}
	listen.Flags().IntVar(&limit, "limit", 20, "Maximum number of events")
	cdp.AddCommand(listen)
	root.AddCommand(cdp)
	root.AddCommand(&cobra.Command{Use: "serve", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		return browser.Serve(ctx)
	}})
	return root
}
