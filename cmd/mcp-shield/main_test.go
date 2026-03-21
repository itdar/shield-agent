package main

import (
	"context"
	"testing"
)

func TestBuildRootCmdFlags(t *testing.T) {
	flags := &globalFlags{}
	cmd := buildRootCmd(flags)

	pf := cmd.PersistentFlags()

	cases := []struct {
		name         string
		defaultValue string
	}{
		{"config", "mcp-shield.yaml"},
		{"log-level", ""},
		{"monitor-addr", ""},
	}
	for _, tc := range cases {
		f := pf.Lookup(tc.name)
		if f == nil {
			t.Errorf("flag --%s not registered", tc.name)
			continue
		}
		if f.DefValue != tc.defaultValue {
			t.Errorf("flag --%s: want default %q, got %q", tc.name, tc.defaultValue, f.DefValue)
		}
	}

	boolCases := []struct {
		name         string
		defaultValue string
	}{
		{"telemetry", "false"},
		{"verbose", "false"},
	}
	for _, tc := range boolCases {
		f := pf.Lookup(tc.name)
		if f == nil {
			t.Errorf("flag --%s not registered", tc.name)
			continue
		}
		if f.DefValue != tc.defaultValue {
			t.Errorf("flag --%s: want default %q, got %q", tc.name, tc.defaultValue, f.DefValue)
		}
	}
}

func TestBuildLogsCmdFlags(t *testing.T) {
	flags := &globalFlags{}
	cmd := buildLogsCmd(flags)

	f := cmd.Flags()

	intCases := []struct {
		name         string
		defaultValue string
	}{
		{"last", "50"},
	}
	for _, tc := range intCases {
		fl := f.Lookup(tc.name)
		if fl == nil {
			t.Errorf("flag --%s not registered", tc.name)
			continue
		}
		if fl.DefValue != tc.defaultValue {
			t.Errorf("flag --%s: want default %q, got %q", tc.name, tc.defaultValue, fl.DefValue)
		}
	}

	strCases := []struct {
		name         string
		defaultValue string
	}{
		{"agent", ""},
		{"since", ""},
		{"method", ""},
		{"format", "table"},
	}
	for _, tc := range strCases {
		fl := f.Lookup(tc.name)
		if fl == nil {
			t.Errorf("flag --%s not registered", tc.name)
			continue
		}
		if fl.DefValue != tc.defaultValue {
			t.Errorf("flag --%s: want default %q, got %q", tc.name, tc.defaultValue, fl.DefValue)
		}
	}
}

func TestInitFromFlagsDefaults(t *testing.T) {
	flags := &globalFlags{configFile: "/nonexistent/mcp-shield.yaml"}
	cfg, logger, err := initFromFlags(flags)
	if err != nil {
		t.Fatalf("initFromFlags returned error: %v", err)
	}
	if logger == nil {
		t.Fatal("initFromFlags returned nil logger")
	}
	if cfg.Security.Mode != "open" {
		t.Errorf("want mode %q, got %q", "open", cfg.Security.Mode)
	}
}

func TestInitFromFlagsVerbose(t *testing.T) {
	flags := &globalFlags{
		configFile: "/nonexistent/mcp-shield.yaml",
		verbose:    true,
	}
	cfg, _, err := initFromFlags(flags)
	if err != nil {
		t.Fatalf("initFromFlags returned error: %v", err)
	}
	if cfg.Logging.Level != "debug" {
		t.Errorf("want log level %q, got %q", "debug", cfg.Logging.Level)
	}
}

func TestInitFromFlagsLogLevel(t *testing.T) {
	flags := &globalFlags{
		configFile: "/nonexistent/mcp-shield.yaml",
		logLevel:   "error",
	}
	cfg, _, err := initFromFlags(flags)
	if err != nil {
		t.Fatalf("initFromFlags returned error: %v", err)
	}
	if cfg.Logging.Level != "error" {
		t.Errorf("want log level %q, got %q", "error", cfg.Logging.Level)
	}
}

func TestRunWrapperNoChild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := runWrapper(ctx, &globalFlags{configFile: "/nonexistent/mcp-shield.yaml"}, []string{})
	if err == nil {
		t.Fatal("expected error for empty child args, got nil")
	}
}
