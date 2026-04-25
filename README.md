# read_cpu

在每天固定的 `00:00:00 UTC+0` 前 1 分钟到后 5 分钟，实时读取 CPU 和内存使用情况。

监控窗口固定为：

- 开始：`23:59:00 UTC`
- 结束：`00:05:00 UTC`

默认采样间隔：

- 每 `1` 秒一次

输出文件：

- `logs/YYYY-MM-DD.csv`
- `logs/YYYY-MM-DD.jsonl`

其中 `YYYY-MM-DD` 是 `00:00:00 UTC` 所属日期。例如：

- 监控窗口：`2026-04-25 23:59:00 UTC` 到 `2026-04-26 00:05:00 UTC`
- 输出文件：`logs/2026-04-26.csv` 和 `logs/2026-04-26.jsonl`

## 运行方式

直接运行：

```bash
cd read_cpu
python3 monitor.py
```

自定义采样间隔：

```bash
python3 monitor.py --interval-seconds 2
```

自定义日志目录：

```bash
python3 monitor.py --log-dir /path/to/output
```

## 输出字段

CSV 和 JSONL 都包含这些字段：

- `timestamp_utc`
- `cpu_percent`
- `mem_used_percent`
- `mem_total_kb`
- `mem_available_kb`
- `mem_used_kb`
- `swap_total_kb`
- `swap_free_kb`
- `swap_used_kb`
- `swap_used_percent`

## 推荐部署

### 方案 1：systemd user service

新建 `~/.config/systemd/user/read_cpu.service`：

```ini
[Unit]
Description=Daily UTC midnight CPU and memory monitor

[Service]
Type=simple
WorkingDirectory=/absolute/path/to/read_cpu
ExecStart=/usr/bin/python3 /absolute/path/to/read_cpu/monitor.py
Restart=always
RestartSec=10

[Install]
WantedBy=default.target
```

启用：

```bash
systemctl --user daemon-reload
systemctl --user enable --now read_cpu.service
```

### 方案 2：tmux / screen / nohup

```bash
cd /absolute/path/to/read_cpu
nohup python3 monitor.py > monitor.out 2>&1 &
```

## 实现说明

- CPU 使用率来自 `/proc/stat`
- 内存和 swap 使用量来自 `/proc/meminfo`
- 不依赖第三方 Python 包，适合 WSL / Linux 直接运行
