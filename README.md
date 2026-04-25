# read_cpu

在每天固定的 `00:00:00 UTC+0` 前 1 分钟到后 5 分钟，实时读取 CPU、内存、网络和磁盘状态。

监控窗口固定为：

- 开始：`23:59:00 UTC`
- 结束：`00:05:00 UTC`

默认采样间隔：

- 每 `100ms` 一次

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
- `net_rx_bytes`
- `net_tx_bytes`
- `net_rx_packets`
- `net_tx_packets`
- `net_rx_errors`
- `net_tx_errors`
- `net_rx_bytes_per_sec`
- `net_tx_bytes_per_sec`
- `net_rx_packets_per_sec`
- `net_tx_packets_per_sec`
- `disk_total_bytes`
- `disk_used_bytes`
- `disk_free_bytes`
- `disk_used_percent`

## 写盘方式

监控窗口内的数据先缓存在内存里：

- 采样过程中不会持续写磁盘
- 等窗口结束后一次性写出
- 最终生成：
  - `logs/YYYY-MM-DD.csv`
  - `logs/YYYY-MM-DD.jsonl`

## Ubuntu 一键运行

在全新的 Ubuntu / WSL Ubuntu 上，直接执行：

```bash
cd read_cpu
bash install_ubuntu.sh
```

这个脚本会：

- 安装 `python3`
- 生成 `systemd --user` service 文件
- 启动并设置开机自启

如果你只想直接前台运行：

```bash
cd read_cpu
bash run.sh
```

## 推荐部署

### 方案 1：systemd user service

自动生成：

```bash
python3 monitor.py --service-file ~/.config/systemd/user/read_cpu.service
systemctl --user daemon-reload
systemctl --user enable --now read_cpu.service
```

### 方案 2：tmux / screen / nohup

```bash
cd /absolute/path/to/read_cpu
nohup bash run.sh > monitor.out 2>&1 &
```

## 实现说明

- CPU 使用率来自 `/proc/stat`
- 内存和 swap 使用量来自 `/proc/meminfo`
- 网络状态来自 `/proc/net/dev`，默认汇总所有非回环网卡
- 磁盘状态来自根分区 `/` 的 `disk_usage`
- 不依赖第三方 Python 包，适合 WSL / Linux 直接运行
