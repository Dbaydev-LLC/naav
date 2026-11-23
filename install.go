package main

import (
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const launchLabel = "dev.naav.naavd"

func escapeXML(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// launchPlist renders a LaunchAgent that starts naavd at login and restarts it if it dies.
func launchPlist(bin string, port int, logPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>--port</string>
    <string>%d</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Background</string>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, launchLabel, escapeXML(bin), port, escapeXML(logPath), escapeXML(logPath))
}

func dataDir() (string, error) {
	if d := os.Getenv("NAAV_DIR"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".naav"), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func launchctl(args ...string) error {
	out, err := exec.Command("launchctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchLabel+".plist"), nil
}

// runInstall copies the binary to ~/.naav/bin and registers a login LaunchAgent.
func runInstall(args []string, defPort int) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	port := fs.Int("port", defPort, "port the service listens on")
	fs.Parse(args)

	dir, err := dataDir()
	if err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		return err
	}
	dst := filepath.Join(binDir, "naavd")
	pp, err := plistPath()
	if err != nil {
		return err
	}
	domain := fmt.Sprintf("gui/%d", os.Getuid())

	// Replace a previous install first so its port is free for the check below.
	_ = launchctl("bootout", domain+"/"+launchLabel)

	if ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port)); err != nil {
		return fmt.Errorf("port %d is busy (is a foreground `./naavd` still running? stop it with Ctrl-C and retry): %w", *port, err)
	} else {
		ln.Close()
	}

	if self != dst {
		if err := copyFile(self, dst); err != nil {
			return fmt.Errorf("copying binary: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(pp), 0o755); err != nil {
		return err
	}
	plist := launchPlist(dst, *port, filepath.Join(dir, "naavd.log"))
	if err := os.WriteFile(pp, []byte(plist), 0o644); err != nil {
		return err
	}
	if err := launchctl("bootstrap", domain, pp); err != nil {
		return err
	}
	fmt.Printf("Installed. Naav now starts at login and runs at http://naav.localhost:%d/\n", *port)
	fmt.Printf("  binary: %s\n  agent:  %s\n  log:    %s\n", dst, pp, filepath.Join(dir, "naavd.log"))
	fmt.Println("Remove with: naavd uninstall   (your aliases in the data folder are kept)")
	return nil
}

func runUninstall() error {
	pp, err := plistPath()
	if err != nil {
		return err
	}
	_ = launchctl("bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), launchLabel))
	if err := os.Remove(pp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	dir, err := dataDir()
	if err == nil {
		os.Remove(filepath.Join(dir, "bin", "naavd"))
		os.Remove(filepath.Join(dir, "bin"))
	}
	fmt.Println("Uninstalled the login service. Aliases are kept in", dir)
	return nil
}
