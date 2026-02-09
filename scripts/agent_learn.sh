#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(
  cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." >/dev/null 2>&1
  pwd
)"
cd "$ROOT_DIR"

ts="$(date +"%Y%m%d-%H%M%S")"
out_dir="artifacts/agent-learning/${ts}"
mkdir -p "$out_dir"

log_file="${out_dir}/quality.log"

set +e
./scripts/go_quality_check.sh >"$log_file" 2>&1
rc=$?
set -e

status="PASS"
if [[ "$rc" -ne 0 ]]; then
  status="FAIL(rc=${rc})"
fi

summary="${out_dir}/summary.md"
{
  echo "## ${ts} 质量检查与学习摘要"
  echo
  echo "- 结果：${status}"
  echo "- 日志：\`${log_file}\`"
  echo
  if [[ "$status" == "PASS" ]]; then
    echo "- 本次未发现需要修复的质量问题。"
  else
    echo "- 本次检查失败：请查看日志并修复后重新执行 \`./scripts/agent_learn.sh\`。"
  fi
  echo
} >"$summary"

agents_file="AGENTS.md"
if [[ ! -f "$agents_file" ]]; then
  echo "[agent_learn] missing ${agents_file}, aborting append" >&2
  exit 2
fi

start_marker="<!-- LEARNING_LOG_START -->"
end_marker="<!-- LEARNING_LOG_END -->"

tmp_file="$(mktemp)"
awk -v start="$start_marker" -v end="$end_marker" -v summary_file="$summary" '
  BEGIN { inserted=0; inblock=0 }
  {
    print $0
    if ($0 ~ start) { inblock=1; next }
    if (inblock && !inserted) {
      while ((getline line < summary_file) > 0) print line
      close(summary_file)
      inserted=1
      inblock=0
    }
  }
  END {
    if (inserted==0) {
      # Markers missing or unexpected layout.
      exit 3
    }
  }
' "$agents_file" >"$tmp_file" || {
  rm -f "$tmp_file"
  echo "[agent_learn] failed to append (missing markers?)" >&2
  exit 3
}

mv "$tmp_file" "$agents_file"
echo "[agent_learn] appended summary into ${agents_file}"
echo "[agent_learn] saved artifacts into ${out_dir}"
exit "$rc"

