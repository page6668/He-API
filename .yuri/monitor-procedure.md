# Yuri Phase B 监控流程 (orchestrix-He-API)

> 由 `/loop 5m` 调用。每 5 分钟一轮，按下列步骤检查 4 窗口 Agent 状态。

## 上下文
- **会话名**: `orchestrix-He-API`
- **窗口**: W0=Arch, W1=SM, W2=Dev, W3=QA
- **HANDOFF 日志**: `/tmp/orchestrix-He-API-handoff.log`
- **状态文件**: `.yuri/state/monitor-state.json`
- **HARD CONSTRAINT**: 不在 Yuri 自己窗口运行 `/clear` 或 `/o {agent}`；所有 Agent 命令通过 `tmux send-keys` 发送给目标窗口。

## 步骤

### 1. 会话健康
```bash
tmux has-session -t orchestrix-He-API 2>/dev/null || echo "SESSION_GONE"
```
若会话消失 → 立即报告用户并停止循环（不要自动重启）。

### 2. 4 窗口快照
对每个 W ∈ {0,1,2,3}：
```bash
tmux capture-pane -t "orchestrix-He-API:$W" -p -S -40 | tail -30 > /tmp/yuri-snap-w$W.txt
md5 -q /tmp/yuri-snap-w$W.txt
```
观察特征：
- `Baked for|Sautéed for|Churned for|Cooked for` 等 → 任务完成空闲
- `Running…` / `tokens` / `thought for` → 正在执行
- `◐` → 审批提示，**自动 y+Enter** 推动
- `Bash error` / `panic` / `FAIL` → 报告
- `❯ ` 空提示符 + 菜单 → 闲置等待

### 3. 进程健康
```bash
for W in 0 1 2 3; do
  PID=$(tmux display-message -t "orchestrix-He-API:$W" -p '#{pane_pid}')
  ALIVE=$(pgrep -P "$PID" -f "claude" | head -1)
  [ -z "$ALIVE" ] && echo "W$W DEAD"
done
```
Claude 进程死亡 → 重启：`tmux send-keys -t :$W "claude" Enter` → sleep 12 → `/o {agent}` Enter。

### 4. Handoff 链
```bash
tail -15 /tmp/orchestrix-He-API-handoff.log
```
- 找最近 `Command: *xxx` 行 + 时间戳
- `ERROR: Source and target are same window` → 误判，可忽略（Dev 自身文本里提到下一步命令时会触发）
- 距今 > 15min 无新 HANDOFF 但 Dev 窗口仍 `Running…` → 正常（长任务）
- 距今 > 15min 无新 HANDOFF 且所有窗口空闲 → 链断了，需要干预

### 5. 故事进度
```bash
done=$(for f in docs/stories/*.md; do
  awk '/^## Status/{f=1; next} f && /[A-Za-z]/{print; exit}' "$f"
done | grep -c "^Done")
total=$(ls docs/stories/*.md | wc -l | tr -d ' ')
echo "$done/$total"
```

### 6. 卡死检测
读 `.yuri/state/monitor-state.json`（结构见末尾）：
- 比对每窗口 hash。若与上轮相同 → `unchanged_rounds++`，否则归 0 并更新 `last_change_ts`
- `unchanged_rounds >= 3` (=15min) 且 Claude 未显示 "Running" → 视为卡死
- **例外**: Dev 窗口运行长测试/构建可能 > 15min 无变化但 `Running…` 仍在显示 → 不算卡死

### 7. 恢复策略（仅在确认卡死时）
| stuck_count | 动作 |
|-------------|------|
| 1 | 重发最近一次 HANDOFF 命令 到目标窗口 |
| 2 | 抓全量诊断 (`-S -200`) + 尝试更深恢复 |
| 3 | 升级用户：详细报告 + 暂停自动恢复 |
| > 3 | 暂停监控循环，等用户介入 |

**发送命令时严格 3 步**：
```bash
tmux send-keys -t "orchestrix-He-API:$W" "<cmd>"
sleep 1
tmux send-keys -t "orchestrix-He-API:$W" Enter
```

### 8. 更新状态
重写 `.yuri/state/monitor-state.json`：
```json
{
  "iteration": 3,
  "last_run_ts": "2026-05-13T16:05:00",
  "windows": {
    "0": {"hash": "abc", "unchanged_rounds": 0, "last_change_ts": "..."},
    "1": {"hash": "def", "unchanged_rounds": 1, "last_change_ts": "..."},
    "2": {"hash": "ghi", "unchanged_rounds": 0, "last_change_ts": "..."},
    "3": {"hash": "jkl", "unchanged_rounds": 0, "last_change_ts": "..."}
  },
  "stuck_count": 0,
  "stories_done": 8,
  "stories_total": 9,
  "last_handoff": {"ts": "2026-05-13T13:18:07", "cmd": "*develop-story 2.3", "to": "dev"}
}
```

### 9. 中文简报（必须 ≤ 8 行）
```
📊 [#N | HH:MM] 进度 X/9 | Dev 运行中 Pm Ys
🟢 W0 Arch 空闲 | W1 SM 空闲 | 🔵 W2 Dev *develop-story 2.3 (P1) | 🟢 W3 QA 空闲
🎯 最近 HANDOFF: 13:18 SM→Dev *develop-story 2.3
⚠️ / ✅ / 🚨 干预动作（若有）
```

## 禁忌
- ❌ 在 Yuri 自己窗口运行 `/clear`、`/o agent`、`cc`
- ❌ 打扰正在 Running 的 Dev（除非真卡死 ≥ 15min）
- ❌ 同窗口 HANDOFF 误判（log "Source and target are same window"）当成需路由
- ❌ Dev 长时间无变化但仍 `Running…` 当作卡死
