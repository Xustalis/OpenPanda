#!/usr/bin/env bash
set -euo pipefail

# Generate an isolated three-node lab. No secret is committed; callers may
# override the generated value with OPENPANDA_SHARED_SECRET.
out="${1:-.lab/three-node}"
secret="${OPENPANDA_SHARED_SECRET:-lab-secret-change-me}"
mkdir -p "$out/entry" "$out/agent" "$out/tools"

write_config() {
  local path="$1" name="$2" addr="$3" root="$4" class="$5" peer1="$6" peer2="$7"
  printf '%s\n' \
    "node:" "  name: \"$name\"" "  resource_class: \"$class\"" \
    "  kind: \"vm\"" "  identity: \"local-lab-$name\"" \
    "network:" "  listen_addr: \"$addr\"" "  shared_secret: \"$secret\"" \
    "  peers:" "    - \"$peer1\"" "    - \"$peer2\"" \
    "storage:" "  db_path: \"$root/openpanda.db\"" "  context_path: \"$root/context\"" \
    "  memory_path: \"$root/memory\"" "  projects_path: \"$root/projects\"" \
    "  skills_path: \"$root/skills\"" "  work_path: \"$root/work\"" \
    "log:" "  level: \"debug\"" > "$path"
}

write_config "$out/entry/config.yaml" entry 127.0.0.1:17801 "$out/entry/data" Standard 127.0.0.1:17802 127.0.0.1:17803
write_config "$out/agent/config.yaml" agent 127.0.0.1:17802 "$out/agent/data" Standard 127.0.0.1:17801 127.0.0.1:17803
write_config "$out/tools/config.yaml" tools 127.0.0.1:17803 "$out/tools/data" Micro 127.0.0.1:17801 127.0.0.1:17802

cat >> "$out/agent/config.yaml" <<'EOF'
model:
  api_type: anthropic
  base_url: "http://127.0.0.1:17810"
  api_key: "scenario-only"
  model: "deterministic-supervisor"
EOF

printf '%s\n' \
  'device: entry' 'resource_class: Standard' 'native: []' 'agents: {}' 'manual: []' \
  'capacity:' '  cpu_cores: 4' '  ram_gb: 8' '  max_concurrent_tasks: 2' > "$out/entry/capabilities.yaml"

printf '%s\n' \
  'device: agent' 'resource_class: Standard' 'native: []' \
  'agents:' '  scenario:' '    adapter: long_task.py' '    capabilities: ["code:modify", "long_task"]' \
  '    cost_tier: low' '    tier: 1' 'manual: []' 'capacity:' \
  '  cpu_cores: 8' '  ram_gb: 16' '  max_concurrent_tasks: 2' > "$out/agent/capabilities.yaml"

printf '%s\n' \
  'device: tools' 'resource_class: Micro' 'native:' \
  '  - id: sys:lab' '    command: uname' '    args: ["-a"]' '    tier: 1' \
  'manual: []' 'capacity:' '  cpu_cores: 2' '  ram_gb: 2' '  max_concurrent_tasks: 1' > "$out/tools/capabilities.yaml"

printf '%s\n' "generated lab at $out" "replace the shared secret before real-device use"
