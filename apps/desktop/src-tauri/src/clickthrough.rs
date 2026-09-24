#[link(name = "user32")]
extern "system" {
    fn GetWindowLongW(h: isize, index: i32) -> i32;
    fn SetWindowLongW(h: isize, index: i32, value: i32) -> i32;
    fn EnumWindows(cb: isize, lparam: isize) -> i32;
    fn GetWindowThreadProcessId(h: isize, pid: *mut u32) -> u32;
    fn GetCurrentProcessId() -> u32;
}
const GWL_EXSTYLE: i32 = -20;
const WS_EX_TRANSPARENT: i32 = 0x0000_0020;
const WS_EX_NOACTIVATE: i32 = 0x0800_0000;
static mut HEALED: i32 = 0;
static mut MYPID: u32 = 0;

unsafe extern "system" fn on_window(h: isize, _l: isize) -> i32 {
    let mut pid: u32 = 0;
    GetWindowThreadProcessId(h, &mut pid);
    if pid == MYPID {
        let ex = GetWindowLongW(h, GWL_EXSTYLE);
        if ex & (WS_EX_TRANSPARENT | WS_EX_NOACTIVATE) != 0 {
            SetWindowLongW(h, GWL_EXSTYLE, ex & !(WS_EX_TRANSPARENT | WS_EX_NOACTIVATE));
            HEALED += 1;
        }
    }
    1
}

/// 部分覆盖层/截图/游戏工具会给其它窗口挂点击穿透样式（实测主窗口被
/// 加 WS_EX_TRANSPARENT|LAYERED|NOACTIVATE——鼠标整窗穿透、键盘正常）。
/// 启动时干净、属运行中注入；每轮状态轮询用纯 Win32 枚举本进程顶层
/// 窗口自检剥掉（不依赖 tauri 的 hwnd 派发——实测该路径拿不到句柄）。
pub fn heal() -> usize {
    unsafe {
        HEALED = 0;
        MYPID = GetCurrentProcessId();
        EnumWindows(on_window as isize, 0);
        HEALED as usize
    }
}
