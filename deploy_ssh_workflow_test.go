package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-yaml"
	"golang.org/x/crypto/ssh"
)

// Execute the actual workflow setup blocks without permitting any network
// probing. Multiple unauthenticated ssh-keyscan connections can ban the runner
// before its first authenticated connection on a Fail2ban-protected server.
func TestCodexProxyNewSSHSetup(t *testing.T) {
	var workflow struct {
		Env  map[string]string `yaml:"env"`
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	data, err := os.ReadFile(".github/workflows/deploy-ssh-codexproxynew.yml")
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		jobName    string
		knownHosts string
	}{
		{jobName: "deploy"},
		{jobName: "rollback"},
		{jobName: "deploy", knownHosts: "configured-host ssh-ed25519 test-configured-key"},
		{jobName: "rollback", knownHosts: "configured-host ssh-ed25519 test-configured-key"},
		// 2026-11-23 coder(lq): 已有当前地址的记录（包括冲突密钥）不能被自动覆盖或追加信任。
		{jobName: "deploy", knownHosts: "deploy.invalid ssh-ed25519 test-conflicting-key"},
		{jobName: "rollback", knownHosts: "deploy.invalid ssh-ed25519 test-conflicting-key"},
	} {
		name := tc.jobName + "/pinned-key"
		if tc.knownHosts != "" {
			name = tc.jobName + "/configured-known-hosts"
		}
		t.Run(name, func(t *testing.T) {
			job, ok := workflow.Jobs[tc.jobName]
			if !ok {
				t.Fatalf("missing job %q", tc.jobName)
			}
			var setup, connect string
			for _, step := range job.Steps {
				switch step.Name {
				case "Install SSH key":
					setup = step.Run
				case "Deploy binary", "Roll back release":
					connect = step.Run
				}
			}
			if setup == "" || connect == "" {
				t.Fatal("missing SSH setup or connection step")
			}

			home := t.TempDir()
			bin := filepath.Join(home, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"ssh-keyscan", "ssh", "curl"} {
				stub := "#!/bin/sh\necho 'SSH setup must not probe the deployment server' >&2\nexit 99\n"
				if err := os.WriteFile(filepath.Join(bin, name), []byte(stub), 0700); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", "-eu", "-c", setup)
			cmd.Env = []string{
				"HOME=" + home,
				"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
				"DEPLOY_HOST=deploy.invalid",
				"DEPLOY_KNOWN_HOSTS=" + tc.knownHosts,
				"DEPLOY_SSH_KEY=test-only-not-a-private-key",
				"DEPLOY_HOST_PUBLIC_KEY=" + workflow.Env["DEPLOY_HOST_PUBLIC_KEY"],
			}
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("SSH setup failed: %v\n%s", err, out)
			}

			publicKey, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(workflow.Env["DEPLOY_HOST_PUBLIC_KEY"]))
			if err != nil || len(bytes.TrimSpace(rest)) != 0 {
				t.Fatalf("invalid pinned server public key: %v", err)
			}
			if publicKey.Type() != ssh.KeyAlgoED25519 {
				t.Fatalf("unexpected host key type %q", publicKey.Type())
			}
			knownHosts, err := os.ReadFile(filepath.Join(home, ".ssh", "known_hosts"))
			if err != nil {
				t.Fatal(err)
			}
			want := "deploy.invalid " + strings.TrimSpace(workflow.Env["DEPLOY_HOST_PUBLIC_KEY"]) + "\n"
			if tc.knownHosts != "" {
				if strings.HasPrefix(tc.knownHosts, "deploy.invalid ") {
					want = tc.knownHosts + "\n"
				} else {
					want = tc.knownHosts + "\n" + want
				}
			}
			if string(knownHosts) != want {
				t.Fatalf("known_hosts does not contain the expected key: %q", knownHosts)
			}
			for path, mode := range map[string]os.FileMode{".ssh": 0700, ".ssh/deploy_key": 0600, ".ssh/known_hosts": 0600} {
				info, err := os.Stat(filepath.Join(home, path))
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != mode {
					t.Errorf("%s permissions: got %o, want %o", path, info.Mode().Perm(), mode)
				}
			}
			for _, option := range []string{"IdentitiesOnly=yes", "ConnectionAttempts=1", "StrictHostKeyChecking=yes"} {
				if !strings.Contains(connect, "-o "+option) {
					t.Errorf("missing SSH option %s", option)
				}
			}
		})
	}
}

// 2026-11-23 coder(lq): 防止旧 Secret 覆盖新 EIP，并验证部署目录取自实际构建源码而非工作流提交。
func TestCodexProxyNewDeploymentTarget(t *testing.T) {
	var workflow struct {
		Env  map[string]string `yaml:"env"`
		Jobs map[string]struct {
			Env   map[string]string `yaml:"env"`
			Steps []struct {
				Name string            `yaml:"name"`
				ID   string            `yaml:"id"`
				Env  map[string]string `yaml:"env"`
				With map[string]string `yaml:"with"`
				Run  string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	data, err := os.ReadFile(".github/workflows/deploy-ssh-codexproxynew.yml")
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	if workflow.Env["DEPLOY_HOST"] != "47.237.8.197" {
		t.Fatal("incorrect deployment IP")
	}
	for name, job := range workflow.Jobs {
		if _, ok := job.Env["DEPLOY_HOST"]; ok {
			t.Fatalf("%s overrides deployment target", name)
		}
		for _, step := range job.Steps {
			if _, ok := step.Env["DEPLOY_HOST"]; ok {
				t.Fatalf("%s/%s overrides deployment target", name, step.Name)
			}
		}
	}
	for _, step := range workflow.Jobs["deploy"].Steps {
		switch step.Name {
		case "Checkout":
			if step.ID != "checkout" || step.With["ref"] != "${{ inputs.release_tag && format('refs/tags/{0}', inputs.release_tag) || github.ref }}" {
				t.Fatal("checkout must honor requested tag")
			}
		case "Deploy binary":
			if step.Env["SOURCE_SHA"] != "${{ steps.checkout.outputs.commit }}" || !strings.Contains(step.Run, "${GITHUB_RUN_NUMBER}-${SOURCE_SHA::7}") {
				t.Fatal("release directory must use checked-out source commit")
			}
		case "Build frontend":
			if step.Env["VITE_APP_VERSION"] != "${{ steps.checkout.outputs.commit }}" {
				t.Fatal("frontend version must use checked-out source commit")
			}
		case "Validate release tag":
			for _, tc := range []struct {
				tag   string
				valid bool
			}{{"", true}, {"v3.1.12", true}, {"v3.1.12-rc.1", true}, {"main", false}, {"v3.1.12;echo bad", false}, {"../v3.1.12", false}} {
				cmd := exec.Command("bash", "-eu", "-c", step.Run)
				cmd.Env = append(os.Environ(), "RELEASE_TAG="+tc.tag)
				if err := cmd.Run(); (err == nil) != tc.valid {
					t.Errorf("tag %q: valid=%v, error=%v", tc.tag, tc.valid, err)
				}
			}
		}
	}
}
