package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/itdar/shield-agent/internal/egress"
)

// buildCACmd builds the `shield-agent ca ...` command tree.
// init generates a fresh CA PEM pair so operators don't have to know
// openssl. trust is a best-effort OS integration for local dev.
func buildCACmd(flags *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ca",
		Short: "Manage the egress MITM CA certificate",
		Long: `CA management utilities for the Phase 2 egress TLS MITM path.
The CA is only used when egress.mitm_hosts is non-empty in the config.`,
	}
	cmd.AddCommand(buildCAInitCmd(flags))
	cmd.AddCommand(buildCATrustCmd(flags))
	return cmd
}

func buildCAInitCmd(flags *globalFlags) *cobra.Command {
	var (
		certPath string
		keyPath  string
		days     int
		force    bool
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Generate a fresh CA certificate and key",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := initFromFlags(flags)
			if err != nil {
				return err
			}
			if certPath == "" {
				certPath = cfg.Egress.CACert
			}
			if keyPath == "" {
				keyPath = cfg.Egress.CAKey
			}
			if certPath == "" || keyPath == "" {
				return fmt.Errorf("ca init: --cert and --key (or egress.ca_cert / ca_key in config) required")
			}
			if days == 0 {
				days = cfg.Egress.CAValidityDays
			}
			if !force {
				if _, err := os.Stat(certPath); err == nil {
					return fmt.Errorf("ca init: %s exists; pass --force to overwrite", certPath)
				}
			}
			ca, err := egress.GenerateCA(certPath, keyPath, days)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "wrote CA certificate to %s (valid until %s)\n", certPath, ca.Cert.NotAfter.Format("2006-01-02"))
			fmt.Fprintf(os.Stdout, "wrote CA private key to %s (mode 0600)\n", keyPath)
			fmt.Fprintln(os.Stdout, "next step: install the certificate into your OS trust store — try `shield-agent ca trust`")
			return nil
		},
		SilenceUsage: true,
	}
	f := cmd.Flags()
	f.StringVar(&certPath, "cert", "", "CA certificate output path (default: egress.ca_cert)")
	f.StringVar(&keyPath, "key", "", "CA key output path (default: egress.ca_key)")
	f.IntVar(&days, "days", 0, "validity in days (default: egress.ca_validity_days or 3650)")
	f.BoolVar(&force, "force", false, "overwrite existing files")
	return cmd
}

func buildCATrustCmd(flags *globalFlags) *cobra.Command {
	var certPath string
	cmd := &cobra.Command{
		Use:   "trust",
		Short: "Install the CA certificate into the OS trust store (best-effort)",
		Long: `Adds the shield-agent CA to the system trust store so clients on
this host accept the dynamic MITM certificates. Behaviour per OS:

  darwin : security add-trusted-cert ... /Library/Keychains/System.keychain
  linux  : copy into /usr/local/share/ca-certificates and run update-ca-certificates
  other  : prints instructions — you must do the install yourself.

Requires root/admin on all platforms. Safe to re-run.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := initFromFlags(flags)
			if err != nil {
				return err
			}
			if certPath == "" {
				certPath = cfg.Egress.CACert
			}
			if certPath == "" {
				return fmt.Errorf("ca trust: --cert (or egress.ca_cert) required")
			}
			if _, err := os.Stat(certPath); err != nil {
				return fmt.Errorf("ca trust: %s not readable: %w", certPath, err)
			}
			return installCATrust(certPath)
		},
		SilenceUsage: true,
	}
	cmd.Flags().StringVar(&certPath, "cert", "", "CA certificate path (default: egress.ca_cert)")
	return cmd
}

// installCATrust dispatches to the OS-specific installer.
func installCATrust(certPath string) error {
	switch runtime.GOOS {
	case "darwin":
		return runCommand("sudo", "security", "add-trusted-cert", "-d", "-r", "trustRoot",
			"-k", "/Library/Keychains/System.keychain", certPath)
	case "linux":
		dst := "/usr/local/share/ca-certificates/shield-agent.crt"
		if err := runCommand("sudo", "cp", certPath, dst); err != nil {
			return err
		}
		return runCommand("sudo", "update-ca-certificates")
	default:
		fmt.Fprintf(os.Stdout, "automatic trust install not supported on %s.\n", runtime.GOOS)
		fmt.Fprintf(os.Stdout, "import %s into your system trust store manually.\n", certPath)
		return nil
	}
}

func runCommand(name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin
	return c.Run()
}
