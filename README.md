# 实时爬取选手战绩工具

### 获取方式
- web_url
https://cwal.gg/players/gateway/30/player/{cwal_gg_id}


- api_url
https://xmploueumzkrdvapbyfs.supabase.co/rest/v1/player_profile_view?select=*&alias=eq.{cwal_gg_id}&gateway=eq.30
Apikey: 
eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJzdXBhYmFzZSIsInJlZiI6InhtcGxvdWV1bXprcmR2YXBieWZzIiwicm9sZSI6ImFub24iLCJpYXQiOjE2NzI4ODY5MTQsImV4cCI6MTk4ODQ2MjkxNH0.p8Jkm2fnFzzy7YYdCs0NVjBdqLmUzvBFJjdf3V0bHuo

Authorization: 
Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJzdXBhYmFzZSIsInJlZiI6InhtcGxvdWV1bXprcmR2YXBieWZzIiwicm9sZSI6ImFub24iLCJpYXQiOjE2NzI4ODY5MTQsImV4cCI6MTk4ODQ2MjkxNH0.p8Jkm2fnFzzy7YYdCs0NVjBdqLmUzvBFJjdf3V0bHuo

### 账号列表（CSV）

选手列表从 `users.csv` 读取，格式为两列：

- `name,cwal_gg_id`

示例：
- `Fengzi,scgotoboy`

### 实现效果

Fengzi 2202 
毕业生 2203 
KId 2202  
xz 2200 
天王 2200 
轩轩 2200 
Messiah 2158 
东海 2210 
小马 2199 
酷酷 2197 
影子鱼2202  
小帅 2209  
过谦 2000
胡阿牛 2100
AP 2207
天王 2200 
迷糊  2205
Messiah 2000
小新 2205 

### 运行方式（Go + GUI）

1. 安装 Go（建议 1.22+）
2. 拉取 Go 依赖（首次必做）：
   - `go get fyne.io/fyne/v2@latest`
   - `go mod tidy`
3. 运行 GUI（窗口可缩放，默认每 5 秒刷新）：
   - `go run .`

如果在 Ubuntu 上运行时报缺少图形/编译依赖（Fyne 常见），安装系统依赖后再运行：

- `sudo apt update`
- `sudo apt install -y gcc pkg-config libgl1-mesa-dev xorg-dev`

说明：
- 程序会先调用 `https://v2.api.cwal.gg/player-update` 触发更新，再调用 `player_profile_view` 读取最新分数。
- 分数读取响应中的 `rating` 字段（不做其他字段兜底）。
- 当前 GUI 继续使用 `Fyne`。
- Windows 构建会优先启用原生透明窗模式（不走 Fyne 窗口层），可实现桌面透出。
- `BG Transparency` 现在会作为 Windows 原生透明窗的初始透明度设置（同一偏好键）。
- Windows 原生透明窗支持快捷键调节透明度：`+` / `↑` 提高不透明度，`-` / `↓` 降低不透明度；调整后会保存到 `BG Transparency`。

### 构建 Windows 可执行文件

在 Windows 本机编译最省事：

- `go build -o cwalgg.exe .`

如果在 Ubuntu 上交叉编译 Windows exe，需要先安装 MinGW 工具链：

1. 安装编译器：
   - `sudo apt update`
   - `sudo apt install -y gcc-mingw-w64-x86-64`
2. 使用 CGO 交叉编译：
   - `CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC=x86_64-w64-mingw32-gcc go build -o cwalgg.exe .`

如果后续出现 C++ 相关报错，可补充：

- `export CXX=x86_64-w64-mingw32-g++`

说明：

- 不能只用 `GOOS=windows GOARCH=amd64 go build`，因为 `Fyne` 依赖 `go-gl`，交叉编译时需要 Windows 的 CGO 工具链。
- `fyne-cross` 也是可选方案，适合后续需要更稳定地打 Windows 包时再引入；当前仓库先保留直接交叉编译方式。
