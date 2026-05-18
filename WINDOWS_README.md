# Windows 版本使用说明

## 编译方法

使用提供的脚本编译（隐藏控制台窗口）：

```bash
./build-windows.sh
```

或手动编译：

```bash
CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC=x86_64-w64-mingw32-gcc \
  go build -ldflags "-H windowsgui" -o cwalgg-windows-amd64.exe .
```

**重要**：`-ldflags "-H windowsgui"` 参数会隐藏控制台窗口，只显示透明覆盖窗口。

## 日志文件

所有 API 请求和更新日志都写入到：

```
api_fetch.log
```

该文件位于程序运行目录下。可以使用任何文本编辑器打开查看。

## 快捷键

- **F2** 或 **右键点击**：打开设置窗口
- **+** / **Up**：降低透明度（更不透明）
- **-** / **Down**：提高透明度（更透明）
- **Esc**：关闭设置/手动输入窗口
- **双击某行**：手动输入该玩家的分数

## 设置说明

### Stop Time（停止时间）

- **清空该字段**：永久持续更新
- **设置未来时间**（格式 `2026-05-18 23:45`）：到达该时间后停止自动更新

### 重新启动更新

如果已经停止更新，想要重新开始：

1. 按 **F2** 打开设置
2. **清空** Stop Time 字段，或设置新的未来时间
3. 点击 **Save**
4. 程序会立即开始更新并记录日志

## 调试

查看 `api_fetch.log` 文件的最后几行，可以了解：

- 程序是否在运行更新
- API 请求是否成功
- 是否达到停止时间
- 设置更改的记录

使用 PowerShell 查看最后 20 行日志：

```powershell
Get-Content -Tail 20 api_fetch.log
```

或使用记事本打开：

```powershell
notepad api_fetch.log
```

## 常见问题

### Q: 为什么分数不更新了？

A: 检查以下几点：
1. 查看 `api_fetch.log` 是否有新的日志
2. Stop Time 是否已经过期
3. 如果 Stop Time 过期，按 F2 清空或设置新时间

### Q: 如何知道程序在运行？

A: 查看 `api_fetch.log` 文件的最后修改时间。如果文件持续更新，说明程序正在运行。

### Q: 出现两个窗口怎么办？

A: 重新使用 `-ldflags "-H windowsgui"` 参数编译，这样只会显示透明覆盖窗口。
