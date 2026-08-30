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

/// 无窗口启动辅助（Windows 控制台不闪窗）。
fn spawn_no_window(bin: &str, args: &[String]) -> Result<Child, String> {
    let mut cmd = Command::new(bin);
    cmd.args(args)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null());
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
    let child = spawn_no_window(&bin, &args)?;
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
                return Err(format!("early_exit: code={}", status.code().unwrap_or(-1)));
            }
        }
    }
    // 健康等待：最多 8s（对齐 ofd run 的 waitHealthy 口径）。
    for _ in 0..50 {
        if http_get(&base, "/healthz").is_some() {
            return Ok(format!("started (pid={pid})"));
        }
        std::thread::sleep(Duration::from_millis(160));
    }
    Err("health_timeout".into())
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
            key_add
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
