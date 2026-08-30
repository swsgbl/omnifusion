// OmniFusion Desktop companion:
// - 网关进程管理（spawn/kill，仅管理自己拉起的实例；外部实例只观测）
// - /healthz 健康探测（纯 std TcpStream，零 HTTP 依赖）
// - `ofd gateway-key` 读取网关 key
// - 设置持久化（app_config_dir/settings.json）
// - 系统托盘：关闭窗口收托盘，退出走托盘菜单
// - i18n：detail/错误串输出中性码（managed/external/stopped/spawn_failed/early_exit/
//   health_timeout/not_managed/gateway_key_*），前端按语言映射；托盘文案经 set_language 切换

use serde::Serialize;
use serde_json::Value;
use std::io::{Read, Write};
use std::net::TcpStream;
use std::process::{Child, Command, Stdio};
use std::sync::Mutex;
use std::time::Duration;
use tauri::{AppHandle, Manager, WindowEvent};
use tauri::menu::{Menu, MenuItem};
use tauri::tray::TrayIconBuilder;

#[cfg(windows)]
use std::os::windows::process::CommandExt;
#[cfg(windows)]
const CREATE_NO_WINDOW: u32 = 0x0800_0000;

/// 本应用拉起的网关子进程句柄（None = 未管理任何实例）。
#[derive(Default)]
struct GatewayProc {
    child: Mutex<Option<Child>>,
}

#[derive(Serialize)]
struct StatusResult {
    running: bool,
    managed: bool,
    detail: String,
}

/// settings.json 路径（app_config_dir/omnifusion-desktop/settings.json）。
fn settings_path(app: &AppHandle) -> Result<std::path::PathBuf, String> {
    let dir = app
        .path()
        .app_config_dir()
        .map_err(|e| format!("app config dir: {e}"))?;
    Ok(dir.join("settings.json"))
}

/// 极简 HTTP GET（仅面向 127.0.0.1 明文端点）：返回响应体或 None。
fn http_get(base: &str, path: &str) -> Option<String> {
    let hostport = base
        .trim_start_matches("http://")
        .trim_start_matches("https://")
        .trim_end_matches('/');
    let hostport = if hostport.contains(':') {
        hostport.to_string()
    } else {
        format!("{hostport}:80")
    };
    let mut stream = TcpStream::connect(&hostport).ok()?;
    stream
        .set_read_timeout(Some(Duration::from_secs(2)))
        .ok()?;
    let req = format!(
        "GET {path} HTTP/1.1\r\nHost: {hostport}\r\nAccept: */*\r\nConnection: close\r\n\r\n"
    );
    stream.write_all(req.as_bytes()).ok()?;
    let mut raw = String::new();
    stream.read_to_string(&mut raw).ok()?;
    let ok = raw.starts_with("HTTP/1.1 200") || raw.starts_with("HTTP/1.0 200");
    if !ok {
        return None;
    }
    Some(raw.split_once("\r\n\r\n").map(|(_, b)| b.to_string()).unwrap_or_default())
}

/// gateway_log_path 解析网关日志文件（安装目录 data/gateway.log）：
/// spawn 的网关 stdout/stderr 重定向到这里——无窗口进程的失败只有
/// 落盘才可见（冷启动早期退出排障的唯一线索）。
fn gateway_log_path() -> Result<std::path::PathBuf, String> {
    let exe = std::env::current_exe().map_err(|e| format!("current_exe: {e}"))?;
    let dir = exe
        .parent()
        .ok_or("current_exe has no parent")?
        .join("data");
    std::fs::create_dir_all(&dir).map_err(|e| format!("mkdir {dir:?}: {e}"))?;
    Ok(dir.join("gateway.log"))
}

/// 读取日志尾部（early_exit/超时错误里附上，排障关键）。
fn log_tail(path: &std::path::Path, max: usize) -> String {
    use std::io::{Read, Seek, SeekFrom};
    let Ok(mut f) = std::fs::File::open(path) else {
        return String::new();
    };
    let len = f.metadata().map(|m| m.len()).unwrap_or(0);
    let start = len.saturating_sub(max as u64);
    if f.seek(SeekFrom::Start(start)).is_err() {
        return String::new();
    }
    let mut buf = String::new();
    let _ = f.read_to_string(&mut buf);
    buf.trim().to_string()
}

/// 无窗口启动辅助：stdout/stderr 重定向到日志文件（无窗口进程的
/// 失败只有落盘才可见）。
fn spawn_no_window(bin: &str, args: &[String], log: Option<&std::path::Path>) -> Result<Child, String> {
    let mut cmd = Command::new(bin);
    cmd.args(args).stdin(Stdio::null());
    match log {
        Some(path) => {
            let f = std::fs::OpenOptions::new()
                .create(true)
                .append(true)
                .open(path)
                .map_err(|e| format!("open log {path:?}: {e}"))?;
            let err = f.try_clone().map_err(|e| format!("clone log fd: {e}"))?;
            cmd.stdout(Stdio::from(f)).stderr(Stdio::from(err));
        }
        None => {
            cmd.stdout(Stdio::null()).stderr(Stdio::null());
        }
    }
    #[cfg(windows)]
    cmd.creation_flags(CREATE_NO_WINDOW);
    cmd.spawn().map_err(|e| format!("spawn_failed: {bin}: {e}"))
}

/// resolve_bin 解析 ofd 可执行路径：用户设置优先；为空时先找安装目录
/// 捆绑副本（tauri resources 的 bin/ofd.exe——桌面安装包自带网关，小白
/// 无需单独下载），再回落 PATH 解析。
fn resolve_bin(app: &AppHandle, bin: &str) -> String {
    let bin = bin.trim();
    if !bin.is_empty() {
        return bin.to_string();
    }
    if let Ok(dir) = app.path().resource_dir() {
        let bundled = dir.join(if cfg!(windows) { "bin/ofd.exe" } else { "bin/ofd" });
        if bundled.is_file() {
            return bundled.to_string_lossy().to_string();
        }
    }
    if cfg!(windows) { "ofd.exe".into() } else { "ofd".into() }
}

#[tauri::command]
fn gateway_status(base: String, state: tauri::State<GatewayProc>) -> StatusResult {
    let mut managed = false;
    if let Ok(mut guard) = state.child.lock() {
        if let Some(child) = guard.as_mut() {
            match child.try_wait() {
                Ok(Some(_)) => *guard = None, // 已退出：回收句柄
                Ok(None) => managed = true,
                Err(_) => *guard = None,
            }
        }
    }
    let running = http_get(&base, "/healthz").is_some();
    StatusResult {
        running,
        managed,
        detail: match (running, managed) {
            (true, true) => "managed".into(),
            (true, false) => "external".into(),
            (false, _) => "stopped".into(),
        },
    }
}

#[tauri::command]
fn gateway_start(
    app: AppHandle,
    bin: String,
    config: String,
    base: String,
    state: tauri::State<GatewayProc>,
) -> Result<String, String> {
    let bin = resolve_bin(&app, &bin);
    if http_get(&base, "/healthz").is_some() {
        return Ok("already-running".into());
    }
    let mut args: Vec<String> = Vec::new();
    if !config.trim().is_empty() {
        args.push("-config".into());
        args.push(config.trim().to_string());
    }
    // 网关日志落盘（安装目录 data/gateway.log）：无窗口进程的失败
    // 只有在这里才可见；早退/超时错误附尾部原文。
    let log_path = gateway_log_path()?;
    let _ = std::fs::write(&log_path, b""); // 每次启动截断，尾部即本次
    let child = spawn_no_window(&bin, &args, Some(&log_path))?;
    let pid = child.id();
    {
        let mut guard = state.child.lock().map_err(|e| e.to_string())?;
        *guard = Some(child);
    }
    // 早退检测：300ms 内即退出多半是二进制/配置路径问题。
    std::thread::sleep(Duration::from_millis(300));
    {
        let mut guard = state.child.lock().map_err(|e| e.to_string())?;
        if let Some(c) = guard.as_mut() {
            if let Ok(Some(status)) = c.try_wait() {
                *guard = None;
                return Err(format!(
                    "early_exit: code={} {}",
                    status.code().unwrap_or(-1),
                    log_tail(&log_path, 600)
                ));
            }
        }
    }
    // 健康等待：20s——重启后首次运行要过杀毒扫描，冷启动可能远超
    // 之前 8s 的窗口（进程活着就继续等；超时不杀，状态栏会自行追上）。
    for _ in 0..100 {
        if http_get(&base, "/healthz").is_some() {
            return Ok(format!("started (pid={pid})"));
        }
        // 等待期间若进程退出，立即报早退而非干等满窗。
        {
            let mut guard = state.child.lock().map_err(|e| e.to_string())?;
            if let Some(c) = guard.as_mut() {
                if let Ok(Some(status)) = c.try_wait() {
                    *guard = None;
                    return Err(format!(
                        "early_exit: code={} {}",
                        status.code().unwrap_or(-1),
                        log_tail(&log_path, 600)
                    ));
                }
            }
        }
        std::thread::sleep(Duration::from_millis(200));
    }
    Err(format!("health_timeout: {}", log_tail(&log_path, 600)))
}

#[tauri::command]
fn gateway_stop(state: tauri::State<GatewayProc>) -> Result<String, String> {
    let mut guard = state.child.lock().map_err(|e| e.to_string())?;
    match guard.take() {
        Some(mut child) => {
            let pid = child.id();
            child.kill().map_err(|e| format!("kill: {e}"))?;
            let _ = child.wait();
            Ok(format!("stopped (pid={pid})"))
        }
        None => Err("not_managed".into()),
    }
}

#[tauri::command]
fn fetch_gateway_key(app: AppHandle, bin: String) -> Result<String, String> {
    let bin = resolve_bin(&app, &bin);
    let mut cmd = Command::new(&bin);
    cmd.arg("gateway-key")
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::null());
    #[cfg(windows)]
    cmd.creation_flags(CREATE_NO_WINDOW);
    let out = cmd.output().map_err(|e| format!("spawn_failed: {bin} gateway-key: {e}"))?;
    if !out.status.success() {
        return Err(format!("gateway_key_failed: code={}", out.status.code().unwrap_or(-1)));
    }
    let key = String::from_utf8_lossy(&out.stdout).trim().to_string();
    if key.is_empty() {
        return Err("gateway_key_empty".into());
    }
    Ok(key)
}

#[tauri::command]
fn load_settings(app: AppHandle) -> Result<Value, String> {
    let path = settings_path(&app)?;
    if !path.exists() {
        return Ok(Value::Null);
    }
    let raw = std::fs::read_to_string(&path).map_err(|e| format!("read {path:?}: {e}"))?;
    serde_json::from_str(&raw).map_err(|e| format!("parse settings: {e}"))
}

#[tauri::command]
fn save_settings(app: AppHandle, settings: Value) -> Result<(), String> {
    let path = settings_path(&app)?;
    if let Some(dir) = path.parent() {
        std::fs::create_dir_all(dir).map_err(|e| format!("mkdir {dir:?}: {e}"))?;
    }
    let body = serde_json::to_string_pretty(&settings).map_err(|e| e.to_string())?;
    std::fs::write(&path, body).map_err(|e| format!("write {path:?}: {e}"))
}

/// key_add 在一个可见控制台窗口里运行 `ofd key add <provider>`（密钥
/// 录入是交互式隐藏输入，必须让用户看见终端；与网关进程无关——落盘
/// 即持久，网关下次取 key 时生效）。fire-and-forget：不等待退出，控制
/// 台由用户完成输入后自行关闭。全局 flag（-config）在子命令之前，
/// 与 gateway_start 的参数序一致。
#[tauri::command]
fn key_add(app: AppHandle, bin: String, config: String, provider: String) -> Result<(), String> {
    if provider.trim().is_empty() {
        return Err("key_add_provider_empty".into());
    }
    let bin = resolve_bin(&app, &bin);
    let mut cmd = Command::new(&bin);
    if !config.trim().is_empty() {
        cmd.arg("-config").arg(config.trim());
    }
    cmd.arg("key").arg("add").arg(provider.trim());
    #[cfg(windows)]
    cmd.creation_flags(0x0000_0010); // CREATE_NEW_CONSOLE：可见交互控制台（密钥隐藏输入）
    cmd.spawn().map_err(|e| format!("spawn_failed: {bin} key add: {e}"))?;
    Ok(())
}

/// client_connect 在可见控制台窗口运行 `ofd connect <cli>`：把网关
/// 地址与令牌确定性写入目标 CLI 的标准配置（备份原文件，控制台显示
/// 写到哪了）。fire-and-forget，同 key_add 的形态。
#[tauri::command]
fn client_connect(app: AppHandle, bin: String, config: String, cli: String) -> Result<(), String> {
    let cli = cli.trim().to_string();
    if !["claude", "codex", "gemini", "opencode"].contains(&cli.as_str()) {
        return Err("client_connect_invalid".into());
    }
    let bin = resolve_bin(&app, &bin);
    let mut cmd = Command::new(&bin);
    if !config.trim().is_empty() {
        cmd.arg("-config").arg(config.trim());
    }
    cmd.arg("connect").arg(&cli);
    #[cfg(windows)]
    cmd.creation_flags(0x0000_0010); // CREATE_NEW_CONSOLE：可见控制台（展示写入结果与备份路径）
    cmd.spawn().map_err(|e| format!("spawn_failed: {bin} connect: {e}"))?;
    Ok(())
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .manage(GatewayProc::default())
        .invoke_handler(tauri::generate_handler![
            gateway_status,
            gateway_start,
            gateway_stop,
            fetch_gateway_key,
            load_settings,
            save_settings,
            set_language,
            key_add,
            client_connect
        ])
        .setup(|app| {
            build_tray(app)?;
            Ok(())
        })
        .on_window_event(|window, event| {
            if let WindowEvent::CloseRequested { api, .. } = event {
                // 关闭 = 收进托盘；真正退出走托盘菜单「退出」。
                let _ = window.hide();
                api.prevent_close();
            }
        })
        .run(tauri::generate_context!())
        .expect("error while running OmniFusion Desktop");
}

/// 托盘菜单文案表（zh/en）。
fn tray_labels(lang: &str) -> (&'static str, &'static str) {
    if lang.starts_with("zh") {
        ("显示主窗口", "退出")
    } else {
        ("Show Main Window", "Quit")
    }
}

/// 按 lang 重建托盘菜单（托盘 id 不变，事件处理器常驻）。
fn rebuild_tray_menu(app: &AppHandle, lang: &str) -> Result<(), Box<dyn std::error::Error>> {
    let (show_label, quit_label) = tray_labels(lang);
    let show = MenuItem::with_id(app, "show", show_label, true, None::<&str>)?;
    let quit = MenuItem::with_id(app, "quit", quit_label, true, None::<&str>)?;
    let menu = Menu::with_items(app, &[&show, &quit])?;
    app.tray_by_id("omnifusion-tray")
        .ok_or("tray not found")?
        .set_menu(Some(menu))?;
    Ok(())
}

/// 启动时从 settings.json 读 lang；缺失/auto 先按 en，前端加载后立刻校正。
fn initial_lang(app: &AppHandle) -> String {
    let lang = settings_path(app)
        .ok()
        .and_then(|p| std::fs::read_to_string(p).ok())
        .and_then(|raw| serde_json::from_str::<Value>(&raw).ok())
        .and_then(|v| v.get("lang").and_then(|l| l.as_str()).map(str::to_string));
    match lang.as_deref() {
        Some(l) if l.starts_with("zh") || l.starts_with("en") => l.to_string(),
        _ => "en".into(),
    }
}

#[tauri::command]
fn set_language(app: AppHandle, lang: String) -> Result<(), String> {
    let lang = if lang.starts_with("zh") { "zh" } else { "en" };
    rebuild_tray_menu(&app, lang).map_err(|e| e.to_string())
}

/// 组装系统托盘：显示主窗口 / 退出。
fn build_tray(app: &tauri::App) -> Result<(), Box<dyn std::error::Error>> {
    let icon = app
        .default_window_icon()
        .ok_or("no default window icon")?
        .clone();
    TrayIconBuilder::with_id("omnifusion-tray")
        .icon(icon)
        .tooltip("OmniFusion Desktop")
        .show_menu_on_left_click(true)
        .on_menu_event(|app, event| match event.id.as_ref() {
            "show" => {
                if let Some(w) = app.get_webview_window("main") {
                    let _ = w.show();
                    let _ = w.set_focus();
                }
            }
            "quit" => app.exit(0),
            _ => {}
        })
        .build(app)?;
    rebuild_tray_menu(&app.handle(), &initial_lang(&app.handle()))?;
    Ok(())
}
