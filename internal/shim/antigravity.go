package shim

import (
	"encoding/json"
	"fmt"
	"strings"
)

// antigravityPluginName is the bundled agy plugin's directory and manifest name.
// `agy plugin install <dir>` copies the bundle into agy's managed plugins
// directory (the exact path is agy-version-specific) keyed by this manifest
// name, so it MUST stay stable across versions or `agy plugin uninstall
// cc-clip-notify` would not find it.
const antigravityPluginName = "cc-clip-notify"

// antigravityPluginVersion is the bundled plugin manifest version. It tracks the
// plugin layout, not the cc-clip binary version. 0.2.0 added the root-level
// named-hook hooks.json that agy 1.4.3+ requires.
const antigravityPluginVersion = "0.2.0"

// antigravityHookName names the single hook in the root hooks.json. agy 1.4.3+
// keys that file by hook name, and `agy plugin install` reports it.
const antigravityHookName = "cc-clip-notify"

// RemoteHasAgy reports whether the Antigravity CLI (agy) is runnable on the
// remote. It probes `command -v agy` rather than a config directory: a present
// ~/.gemini/antigravity-cli/ directory does NOT imply the agy executable is on
// PATH. Tri-state parsing (yes/no/garbage) mirrors RemoteHasCodex.
func RemoteHasAgy(session RemoteExecutor) (bool, error) {
	out, err := session.Exec("if command -v agy >/dev/null 2>&1; then echo yes; else echo no; fi")
	if err != nil {
		return false, fmt.Errorf("failed to check for remote agy CLI: %w", err)
	}
	switch strings.TrimSpace(out) {
	case "yes":
		return true, nil
	case "no":
		return false, nil
	default:
		return false, fmt.Errorf("unexpected agy probe output: %q", out)
	}
}

// antigravityHookCommand builds the Stop-hook command string the agy plugin
// invokes: `cc-clip plugin run agy-notify` (the runner dispatch key,
// AdapterAntigravityNotify). For a non-default port the CC_CLIP_PORT env is
// prepended so the runner reaches the right daemon, mirroring
// codexNotifyManagedBlock.
func antigravityHookCommand(port int) string {
	base := "cc-clip plugin run " + string(AdapterAntigravityNotify)
	if port == 18339 {
		return base
	}
	return fmt.Sprintf("env CC_CLIP_PORT=%d %s", port, base)
}

// antigravityPluginJSON returns the plugin.json manifest. agy 1.0.0 accepts a
// minimal {name, version, description}. Built via struct marshal so the output
// is always valid JSON.
func antigravityPluginJSON() string {
	manifest := struct {
		Name        string `json:"name"`
		Version     string `json:"version"`
		Description string `json:"description"`
	}{
		Name:        antigravityPluginName,
		Version:     antigravityPluginVersion,
		Description: "Forward Antigravity Stop events to the local machine as native notifications via cc-clip.",
	}
	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		// The struct is fixed and always marshalable; this path is unreachable.
		return ""
	}
	return string(b)
}

type antigravityHookHandler struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

func antigravityStopHandlers(port int) map[string][]antigravityHookHandler {
	return map[string][]antigravityHookHandler{
		"Stop": {{Type: "command", Command: antigravityHookCommand(port), Timeout: 10}},
	}
}

// antigravityHooksJSON returns the ROOT hooks.json, the only hook file agy
// 1.4.3+ loads ("plugin hooks are read only from hooks.json at the plugin
// root"). That loader keys the file by hook NAME, each holding its events:
// {"cc-clip-notify": {"Stop": [...]}}. The old flat {"Stop": [...]} shape
// fails to parse there ("cannot unmarshal array into ... JSONHookSpec") and
// the whole file is skipped, which is how agy notifications went silent.
// Verified with agy 1.5.16: `agy plugin validate --strict` counts 1 hook, the
// CLI log shows "loaded 1 named hooks", and a finished turn reaches /notify.
func antigravityHooksJSON(port int) string {
	hooks := map[string]map[string][]antigravityHookHandler{
		antigravityHookName: antigravityStopHandlers(port),
	}
	b, err := json.MarshalIndent(hooks, "", "  ")
	if err != nil {
		return ""
	}
	return string(b)
}

// antigravityLegacyHooksJSON returns hooks/hooks.json in the flat
// {"Stop": [...]} shape for early agy releases (1.0.x), which read only that
// path and ignore the root file. agy 1.4.3+ never reads it (validate prints a
// "never loaded" warning, not an error), so each agy generation loads exactly
// one of the two files and a Stop event is never delivered twice.
func antigravityLegacyHooksJSON(port int) string {
	b, err := json.MarshalIndent(antigravityStopHandlers(port), "", "  ")
	if err != nil {
		return ""
	}
	return string(b)
}

// buildAntigravityPluginScript returns the bash script that stages the plugin
// bundle into a fresh temp source dir and hands it to agy.
//
// Why stage to a throwaway source dir (not write into the final plugins dir
// then install): `agy plugin install <dir>` copies the bundle into agy's
// managed plugins directory and records it in agy's plugin manifest. Pointing
// install at that final dir risks a self-copy and a polluted manifest, so the
// source MUST be a temp dir.
//
// `set -e` plus validate-before-install means a rejected layout aborts before
// install ever runs. The EXIT trap removes the temp source whether the script
// succeeds or fails (agy keeps its own installed copy). agy's stderr is folded
// into stdout (2>&1) so EnsureRemoteAntigravityPlugin can surface the reason on
// failure even though RemoteExecutor.Exec discards stderr.
func buildAntigravityPluginScript(port int) string {
	return fmt.Sprintf(`set -e
mkdir -p ~/.cache/cc-clip
src=$(mktemp -d "$HOME/.cache/cc-clip/agy-plugin-src.XXXXXX")
trap 'rm -rf "$src"' EXIT
pdir="$src/%[1]s"
mkdir -p "$pdir/hooks"
cat > "$pdir/plugin.json" <<'CC_CLIP_AGY_PLUGIN_JSON_EOF'
%[2]s
CC_CLIP_AGY_PLUGIN_JSON_EOF
cat > "$pdir/hooks.json" <<'CC_CLIP_AGY_HOOKS_JSON_EOF'
%[3]s
CC_CLIP_AGY_HOOKS_JSON_EOF
cat > "$pdir/hooks/hooks.json" <<'CC_CLIP_AGY_LEGACY_HOOKS_JSON_EOF'
%[4]s
CC_CLIP_AGY_LEGACY_HOOKS_JSON_EOF
agy plugin validate "$pdir" 2>&1
agy plugin install "$pdir" 2>&1
`, antigravityPluginName, antigravityPluginJSON(), antigravityHooksJSON(port), antigravityLegacyHooksJSON(port))
}

// EnsureRemoteAntigravityPlugin installs (or reinstalls) the bundled
// cc-clip-notify agy plugin on the remote. It stages the bundle to a temp source
// dir, validates the layout, then installs via the agy CLI. Any failure (agy
// missing, validate rejection, install error) is surfaced with the agy output
// for diagnosis.
//
// Note: a successful install only proves the layout is accepted, not that the
// Stop hook fires; the deploy state records Verified=false accordingly.
func EnsureRemoteAntigravityPlugin(session RemoteExecutor, port int) error {
	out, err := session.Exec(buildAntigravityPluginScript(port))
	if err != nil {
		return fmt.Errorf("failed to install %s agy plugin: %s: %w", antigravityPluginName, strings.TrimSpace(out), err)
	}
	return nil
}
